// Package tokenizer owns one optional offline official-tokenizer process per query.
package tokenizer

import (
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
	"unicode/utf8"
)

const (
	MaxBytes  = 512 << 10
	MaxWork   = 32 << 20
	MaxCalls  = 102
	Lifetime  = 15 * time.Second
	handshake = "mousa.tokens.v1/tiktoken/0.12.0/o200k_base/ordinary\n"
)

//go:embed worker.py
var worker string

// Counter is sequential and query-scoped. Finish must succeed before persistence.
type Counter struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output io.ReadCloser
	cancel context.CancelFunc
	stop   func() bool
	calls  int
	work   int
	closed bool
}

// Start loads the immutable encoder without receiving evidence bytes.
func Start(parent context.Context, python, cache string) (*Counter, error) {
	if python == "" || cache == "" {
		return nil, errors.New("tokenizer requires an interpreter and prepared cache")
	}
	ctx, cancel := context.WithTimeout(parent, Lifetime)
	cmd := exec.CommandContext(ctx, python, "-I", "-c", worker)
	cmd.Env = []string{"TIKTOKEN_CACHE_DIR=" + cache}
	cmd.WaitDelay = time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		cancel()
		return nil, err
	}
	c := &Counter{cmd: cmd, input: input, output: output, cancel: cancel}
	if err := cmd.Start(); err != nil {
		input.Close()
		output.Close()
		cancel()
		return nil, fmt.Errorf("tokenizer startup: %w", err)
	}
	c.stop = context.AfterFunc(ctx, func() { input.Close(); output.Close() })
	buf := make([]byte, len(handshake))
	if _, err := io.ReadFull(output, buf); err != nil || string(buf) != handshake {
		c.Close()
		return nil, errors.New("tokenizer unavailable: require local Python with tiktoken==0.12.0 and a prepared o200k_base cache")
	}
	return c, nil
}

// Count measures the entire proposed rendering, never a sum of passage counts.
func (c *Counter) Count(text string) (uint64, error) {
	if c.closed || !utf8.ValidString(text) || len(text) > MaxBytes || c.calls >= MaxCalls || len(text) > MaxWork-c.work {
		return 0, errors.New("tokenizer work or input bound exceeded")
	}
	c.calls++
	c.work += len(text)
	var frame [4]byte
	binary.BigEndian.PutUint32(frame[:], uint32(len(text)))
	if _, err := c.input.Write(frame[:]); err != nil {
		return 0, errors.New("tokenizer input failed")
	}
	if _, err := io.WriteString(c.input, text); err != nil {
		return 0, errors.New("tokenizer input failed")
	}
	var reply [8]byte
	if _, err := io.ReadFull(c.output, reply[:]); err != nil {
		return 0, errors.New("tokenizer reply truncated or deadline exceeded")
	}
	count := binary.BigEndian.Uint64(reply[:])
	if count > uint64(len(text)) || len(text) > 0 && count == 0 {
		return 0, errors.New("tokenizer count outside ordinary-text bounds")
	}
	return count, nil
}

// Finish closes input, requires clean EOF and reaps the worker before commit.
func (c *Counter) Finish() error {
	if c.closed {
		return errors.New("tokenizer already closed")
	}
	c.closed = true
	c.input.Close()
	var extra [1]byte
	n, err := c.output.Read(extra[:])
	waitErr := c.cmd.Wait()
	c.stop()
	c.cancel()
	c.output.Close()
	if n != 0 || err != io.EOF || waitErr != nil {
		return errors.New("tokenizer did not finish cleanly")
	}
	return nil
}

// Close aborts and reaps a failed or unused counter. It is safe after Finish.
func (c *Counter) Close() {
	if c.closed {
		return
	}
	c.closed = true
	c.cancel()
	c.input.Close()
	c.output.Close()
	c.cmd.Wait()
	c.stop()
}
