//go:build linux

package hippo

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// PTY is the master end of a pseudo-terminal whose slave end is linked at a
// path, so that gpsd (or anything that opens a serial device) can read what is
// written to it.
type PTY struct {
	master, slave *os.File
	link          string
}

// OpenPTY creates a pty and a symlink at link to its slave. An existing
// symlink at link is replaced; anything else there is an error.
func OpenPTY(link string) (*PTY, error) {
	m, err := os.OpenFile("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fd := int(m.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		return nil, fmt.Errorf("unlockpt: %w", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("ptsname: %w", err)
	}
	name := fmt.Sprintf("/dev/pts/%d", n)
	// Held open so writes before gpsd attaches do not fail with EIO, and
	// raw so the line discipline leaves the NMEA alone.
	s, err := os.OpenFile(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		m.Close()
		return nil, err
	}
	if t, err := unix.IoctlGetTermios(int(s.Fd()), unix.TCGETS); err == nil {
		t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
		t.Oflag &^= unix.OPOST
		t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
		t.Cflag &^= unix.CSIZE | unix.PARENB
		t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL
		unix.IoctlSetTermios(int(s.Fd()), unix.TCSETS, t)
	}
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			m.Close()
			s.Close()
			return nil, fmt.Errorf("%s exists and is not a symlink", link)
		}
		os.Remove(link)
	}
	if err := os.Symlink(name, link); err != nil {
		m.Close()
		s.Close()
		return nil, err
	}
	return &PTY{master: m, slave: s, link: link}, nil
}

// Write sends p to the slave end. A full buffer (nobody reading) drops the
// data instead of blocking the reader of the module.
func (p *PTY) Write(b []byte) (int, error) {
	n, err := p.master.Write(b)
	if errors.Is(err, syscall.EAGAIN) {
		return len(b), nil
	}
	return n, err
}

// Close removes the link and closes both ends.
func (p *PTY) Close() error {
	os.Remove(p.link)
	p.slave.Close()
	return p.master.Close()
}
