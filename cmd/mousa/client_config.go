package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

// The client configuration lets an agent run plain `mousa query` against a
// served store: it names the endpoint and where the token comes from. Mousa
// stores no token itself; the file or command is the user's choice.

const (
	clientMaxConfigBytes = 16 << 10
	clientTokenTimeout   = 30 * time.Second
	clientHarnessMark    = "{harness}"
)

type clientConfig struct {
	Endpoint     string   `json:"endpoint,omitzero"`
	TokenFile    string   `json:"token_file,omitzero"`
	TokenCommand []string `json:"token_command,omitzero"`
}

// loadClientConfig reads $MOUSA_CONFIG, else mousa/client.json under the user
// configuration directory. Only the default path may be absent.
func loadClientConfig() (clientConfig, error) {
	var config clientConfig
	path := os.Getenv("MOUSA_CONFIG")
	named := path != ""
	if !named {
		dir, err := os.UserConfigDir()
		if err != nil {
			return config, nil
		}
		path = filepath.Join(dir, "mousa", "client.json")
	}
	file, err := os.Open(path)
	if err != nil {
		if !named && errors.Is(err, fs.ErrNotExist) {
			return config, nil
		}
		return config, errors.New("cannot open client configuration")
	}
	defer file.Close()
	data, err := readBoundedInput(file, clientMaxConfigBytes, "client configuration")
	if err != nil {
		return config, err
	}
	if err := decodeNative(data, &config); err != nil {
		return config, errors.New("invalid client configuration")
	}
	if config.TokenFile != "" && len(config.TokenCommand) != 0 {
		return config, errors.New("client configuration sets both token_file and token_command")
	}
	return config, nil
}

// token resolves the credential: MOUSA_TOKEN, else token_file, else the
// output of token_command. Failures never echo a path, argument or value.
func (c clientConfig) token() (string, error) {
	if token := os.Getenv("MOUSA_TOKEN"); token != "" {
		return token, nil
	}
	var raw []byte
	switch {
	case c.TokenFile != "":
		path, err := expandHarness(c.TokenFile)
		if err != nil {
			return "", err
		}
		if rest, ok := strings.CutPrefix(path, "~/"); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", errors.New("cannot resolve the home directory for token_file")
			}
			path = filepath.Join(home, rest)
		}
		file, err := os.Open(path)
		if err != nil {
			return "", errors.New("cannot open the configured token file")
		}
		defer file.Close()
		if raw, err = readBoundedInput(file, nativeMaxTokenBytes+2, "token file"); err != nil {
			return "", errors.New("the configured token file is too large")
		}
	case len(c.TokenCommand) != 0:
		argv := make([]string, len(c.TokenCommand))
		for i, argument := range c.TokenCommand {
			var err error
			if argv[i], err = expandHarness(argument); err != nil {
				return "", err
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), clientTokenTimeout)
		defer cancel()
		command := exec.CommandContext(ctx, argv[0], argv[1:]...)
		command.Stdin = os.Stdin
		command.Stderr = os.Stderr
		var err error
		if raw, err = command.Output(); err != nil {
			return "", errors.New("the configured token command failed")
		}
	default:
		return "", errors.New("no token: set MOUSA_TOKEN, or token_file or token_command in the client configuration")
	}
	token := string(bytes.TrimSpace(raw))
	if token == "" || len(token) > nativeMaxTokenBytes {
		return "", errors.New("the configured token source did not yield a usable token")
	}
	return token, nil
}

// expandHarness substitutes MOUSA_HARNESS for {harness}, so one configuration
// serves several agents. The name is restricted to the principal ID shape,
// which keeps it from walking a path or adding an argument.
func expandHarness(value string) (string, error) {
	if !strings.Contains(value, clientHarnessMark) {
		return value, nil
	}
	harness := os.Getenv("MOUSA_HARNESS")
	if !mousa.ValidPrincipalID(harness) {
		return "", errors.New("the client configuration uses {harness}; set MOUSA_HARNESS to a name like a principal ID")
	}
	return strings.ReplaceAll(value, clientHarnessMark, harness), nil
}

// agentCommand edits the serve configuration offline. It needs no store.
func agentCommand(args []string) error {
	if len(args) < 2 || args[0] != "add" {
		return usageError{"agent takes: add <name> --config <file> --source <id>"}
	}
	name := args[1]
	flags := newCommandFlags("agent add")
	configPath := flags.String("config", "", "serve configuration file (required); created if absent")
	tokenFile := flags.String("token-file", "", "write the token to a new 0600 file instead of printing it")
	operator := flags.Bool("operator", false, "let the principal read request history and the stage stream")
	var read, write []string
	flags.Func("source", "source the agent may read and write; repeatable", func(value string) error {
		read, write = append(read, value), append(write, value)
		return nil
	})
	flags.Func("read", "source the agent may only read; repeatable", func(value string) error {
		read = append(read, value)
		return nil
	})
	flags.Func("write", "source the agent may only write; repeatable", func(value string) error {
		write = append(write, value)
		return nil
	})
	if err := flags.Parse(args[2:]); err != nil {
		return usageError{err.Error()}
	}
	if *configPath == "" || len(flags.Args()) != 0 || len(read)+len(write) == 0 {
		return usageError{"agent add requires <name> --config <file> and at least one --source, --read or --write"}
	}

	var config nativeConfig
	existing, err := os.ReadFile(*configPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return errors.New("cannot read serve configuration")
	case len(existing) > nativeMaxConfigBytes:
		return errors.New("serve configuration is too large")
	default:
		if err := decodeNative(existing, &config); err != nil {
			return errors.New("invalid serve configuration")
		}
	}
	token := rand.Text()
	sum := sha256.Sum256([]byte(token))
	config.Principals = append(config.Principals, nativePrincipalConfig{
		ID: name, TokenSHA256: hex.EncodeToString(sum[:]),
		ReadSources: read, WriteSources: write, Operator: *operator,
	})
	updated, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	updated = append(updated, '\n')
	// The same validation serve runs: a bad name, a repeated ID or a bad
	// source fails here, before anything is written.
	if _, err := parseNativeConfig(updated); err != nil {
		return err
	}

	result := map[string]any{"id": name, "read_sources": read, "write_sources": write, "restart_serve": true}
	if *tokenFile == "" {
		result["token"] = token
	} else {
		// O_EXCL: never replace an existing credential file.
		file, err := os.OpenFile(*tokenFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("cannot create token file: %w", err)
		}
		_, writeErr := file.WriteString(token + "\n")
		if closeErr := file.Close(); writeErr != nil || closeErr != nil {
			os.Remove(*tokenFile)
			return errors.New("cannot write token file")
		}
		result["token_file"] = *tokenFile
	}
	if err := replaceFile(*configPath, updated); err != nil {
		if *tokenFile != "" {
			os.Remove(*tokenFile)
		}
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

// replaceFile swaps in new contents by rename so serve never reads half a file.
func replaceFile(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mousa-config-*")
	if err != nil {
		return errors.New("cannot write serve configuration")
	}
	defer os.Remove(temporary.Name())
	_, writeErr := temporary.Write(data)
	if closeErr := temporary.Close(); writeErr != nil || closeErr != nil {
		return errors.New("cannot write serve configuration")
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return errors.New("cannot replace serve configuration")
	}
	return nil
}
