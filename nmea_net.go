package hippo

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// NMEAServer streams NMEA to whoever connects, for consumers that read it over
// a socket (GeoClue's network-nmea source). Like Tap, a client too slow to keep
// up loses sentences and never holds the reader back. Unlike Tap it is thinned
// to one position set per UTC second, because the module's DR rate is ~10 Hz
// with the same whole-second time and consumers want about one fix a second.
//
// It is an io.Writer: each Write is one sentence, as Run writes them.
type NMEAServer struct {
	path    string // the unix socket to remove on Close, if any
	ln      net.Listener
	mu      sync.Mutex
	clients map[chan string]struct{}
	last    map[string]string // sentence type -> time field last passed
	vtg     bool              // a GGA passed, so its VTG does too
}

// ListenNMEA serves addr: "unix:PATH" (mode 0666, a stale socket is replaced;
// the consumer may be another user) or a TCP host:port.
func ListenNMEA(addr string) (*NMEAServer, error) {
	s := &NMEAServer{clients: map[chan string]struct{}{}, last: map[string]string{}}
	var err error
	if p, ok := strings.CutPrefix(addr, "unix:"); ok {
		if s.ln, err = listenUnix(p); err != nil {
			return nil, err
		}
		s.path = p
	} else if s.ln, err = net.Listen("tcp", addr); err != nil {
		return nil, err
	}
	go s.accept()
	return s, nil
}

func listenUnix(path string) (net.Listener, error) {
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return nil, fmt.Errorf("hippo: %s is already served", path)
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("hippo: %s exists and is not a socket", path)
		}
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o666); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Addr is the address being served.
func (s *NMEAServer) Addr() net.Addr { return s.ln.Addr() }

func (s *NMEAServer) accept() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		ch := make(chan string, 64)
		s.mu.Lock()
		s.clients[ch] = struct{}{}
		s.mu.Unlock()
		go s.serve(c, ch)
	}
}

func (s *NMEAServer) serve(c net.Conn, ch chan string) {
	defer c.Close()
	go func() {
		io.Copy(io.Discard, c)
		s.drop(ch)
	}()
	for line := range ch {
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(c, line); err != nil {
			s.drop(ch)
			return
		}
	}
}

func (s *NMEAServer) drop(ch chan string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[ch]; ok {
		delete(s.clients, ch)
		close(ch)
	}
}

// pass reports whether sentence is sent on. GGA and RMC pass once per time
// field (hhmmss, so once per UTC second), VTG, which has no time, only after
// the GGA that passed with it, and anything else (GSV) always.
func (s *NMEAServer) pass(sentence string) bool {
	if len(sentence) < 7 || sentence[0] != '$' {
		return true
	}
	typ := sentence[3:6]
	switch typ {
	case "GGA", "RMC":
		f, _, _ := strings.Cut(sentence[7:], ",")
		f, _, _ = strings.Cut(f, ".")
		if s.last[typ] == f {
			return false
		}
		s.last[typ] = f
		if typ == "GGA" {
			s.vtg = true
		}
	case "VTG":
		ok := s.vtg
		s.vtg = false
		return ok
	}
	return true
}

// Write sends sentence to every client, without waiting for any.
func (s *NMEAServer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sentence := string(p)
	if !s.pass(sentence) {
		return len(p), nil
	}
	for ch := range s.clients {
		select {
		case ch <- sentence:
		default:
		}
	}
	return len(p), nil
}

// Close stops serving and disconnects the clients.
func (s *NMEAServer) Close() {
	s.ln.Close()
	if s.path != "" {
		os.Remove(s.path)
	}
	s.mu.Lock()
	chs := make([]chan string, 0, len(s.clients))
	for ch := range s.clients {
		chs = append(chs, ch)
	}
	s.mu.Unlock()
	for _, ch := range chs {
		s.drop(ch)
	}
}

// teeWriter writes to both, so that a failing a does not stop b.
type teeWriter struct{ a, b io.Writer }

func (t teeWriter) Write(p []byte) (int, error) {
	n, err := t.a.Write(p)
	t.b.Write(p)
	return n, err
}
