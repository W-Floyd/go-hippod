//go:build linux

package hippo

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The whole command: a pty stands in for the receiver, -out stays a pty/stdout
// and -nmea-tcp serves one set a second.
func TestMainNMEATCP(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no /dev/ptmx")
	}
	dir, err := os.MkdirTemp("", "mn")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	rx, err := OpenPTY(filepath.Join(dir, "rx"))
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer rx.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	var out, errb syncBuf
	code := make(chan int, 1)
	go func() {
		code <- Main([]string{"-dev", filepath.Join(dir, "rx"), "-nmea-tcp", addr}, &out, &errb)
	}()
	var c net.Conn
	for end := time.Now().Add(5 * time.Second); c == nil; time.Sleep(10 * time.Millisecond) {
		if c, _ = net.Dial("tcp", addr); c == nil && time.Now().After(end) {
			t.Fatalf("no listener: %s", errb.String())
		}
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	time.Sleep(100 * time.Millisecond)
	for range 10 {
		rx.Write(Encode(utcBody(0)))
		rx.Write(Encode(packRawDR(48.1173, 11.5, 0x4000, 1000, 0x80)))
	}
	var got string
	for buf := make([]byte, 1024); strings.Count(got, "$GPVTG") < 1; {
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("read: %v; got %q", err, got)
		}
		got += string(buf[:n])
	}
	time.Sleep(200 * time.Millisecond)
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	select {
	case <-code:
	case <-time.After(5 * time.Second):
		t.Fatal("Main did not stop")
	}
	if n := strings.Count(out.String(), "$GPGGA"); n != 10 {
		t.Errorf("-out got %d GGA, want all 10", n)
	}
	if n := strings.Count(got, "$GPGGA"); n != 1 {
		t.Errorf("tcp got %d GGA, want 1: %q", n, got)
	}
}
