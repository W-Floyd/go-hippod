//go:build linux

package hippo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// With nobody reading the slave end, Write must drop, not wait: a blocked write
// stops the whole reader of the module.
func TestPTYWriteDoesNotBlockWithoutReader(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no /dev/ptmx")
	}
	p, err := OpenPTY(filepath.Join(t.TempDir(), "gps0"))
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer p.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		line := []byte("$GPGGA,000000.00,0000.0000,N,00000.0000,E,0,00,99.99,0.0,M,0.0,M,,*00\r\n")
		for range 20000 { // well past a pty's buffer
			if _, err := p.Write(line); err != nil {
				t.Errorf("Write: %v", err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Write blocked with nobody reading the slave")
	}
}
