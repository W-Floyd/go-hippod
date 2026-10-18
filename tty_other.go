//go:build !linux

package hippo

import (
	"errors"
	"os"
)

// OpenTTY is only implemented on Linux.
func OpenTTY(path string, baud int) (*os.File, error) {
	return nil, errors.New("hippo: serial ports are only supported on Linux")
}

// PTY is only implemented on Linux.
type PTY struct{}

// OpenPTY is only implemented on Linux.
func OpenPTY(link string) (*PTY, error) {
	return nil, errors.New("hippo: pty output is only supported on Linux")
}

func (*PTY) Write(b []byte) (int, error) { return len(b), nil }
func (*PTY) Close() error                { return nil }
