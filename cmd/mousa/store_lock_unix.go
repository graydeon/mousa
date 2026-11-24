//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockStoreFile takes a nonblocking exclusive advisory lock on the database
// inode itself, orthogonal to SQLite's own locks. The returned handle must
// stay open for the daemon's lifetime; closing it releases ownership.
func lockStoreFile(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open store for ownership lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("store is already owned by a running serve process")
	}
	return file, nil
}
