//go:build !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd

package main

import (
	"errors"
	"os"
)

// lockStoreFile refuses to run without an advisory lock mechanism rather than
// serving a store it cannot exclusively own.
func lockStoreFile(path string) (*os.File, error) {
	return nil, errors.New("serve store ownership lock is unsupported on this platform")
}
