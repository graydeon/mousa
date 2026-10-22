package tokenizer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakePython(t *testing.T, body string) (string, string) {
	t.Helper()
	python := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON")
	if python == "" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			t.Skip("Python unavailable for protocol fault fixture")
		}
	}
	root := t.TempDir()
	path := filepath.Join(root, "counter")
	pid := filepath.Join(root, "pid")
	script := fmt.Sprintf("#!%s\nimport os,sys,time,struct\nopen(%q,'w').write(str(os.getpid()))\n%s\n", python, pid, body)
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path, pid
}

func assertReaped(t *testing.T, pidPath string) {
	t.Helper()
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return
	} // Cancellation may precede interpreter startup.
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("tokenizer child %d not reaped: %v", pid, err)
	}
}

func TestTokenProtocolFailuresReap(t *testing.T) {
	hello := fmt.Sprintf("sys.stdout.buffer.write(%q.encode());sys.stdout.buffer.flush()\n", handshake)
	for name, body := range map[string]string{
		"wrong handshake": "print('wrong',flush=True)",
		"startup timeout": "time.sleep(30)",
		"EOF":             hello + "sys.stdin.buffer.read(4)",
		"negative":        hello + "sys.stdin.buffer.read(4);sys.stdout.buffer.write(b'\\xff'*8);sys.stdout.buffer.flush()",
		"oversized":       hello + "sys.stdin.buffer.read(4);sys.stdout.buffer.write(struct.pack('>Q',100));sys.stdout.buffer.flush()",
		"truncated":       hello + "sys.stdin.buffer.read(4);sys.stdout.buffer.write(b'\\x00');sys.stdout.buffer.flush()",
		"count timeout":   hello + "sys.stdin.buffer.read(4);time.sleep(30)",
	} {
		t.Run(name, func(t *testing.T) {
			path, pid := fakePython(t, body)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			c, err := Start(ctx, path, t.TempDir())
			if err == nil {
				_, err = c.Count("hello")
				c.Close()
			}
			if err == nil {
				t.Fatal("malformed or failed worker accepted")
			}
			assertReaped(t, pid)
		})
	}
}

func TestTokenFinishRejectsTrailingOutput(t *testing.T) {
	body := fmt.Sprintf("sys.stdout.buffer.write(%q.encode());sys.stdout.buffer.flush()\nsys.stdin.buffer.read()\nsys.stdout.buffer.write(b'x');sys.stdout.buffer.flush()", handshake)
	path, pid := fakePython(t, body)
	c, err := Start(context.Background(), path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Finish(); err == nil {
		t.Fatal("trailing output accepted")
	}
	assertReaped(t, pid)
}

func TestTokenCancellationReaps(t *testing.T) {
	path, pid := fakePython(t, "time.sleep(30)")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c, err := Start(ctx, path, t.TempDir()); err == nil {
		c.Close()
		t.Fatal("cancelled startup accepted")
	}
	assertReaped(t, pid)
}
