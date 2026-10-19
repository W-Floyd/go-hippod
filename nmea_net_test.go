package hippo

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestNMEAServerPass(t *testing.T) {
	gga := func(hms string) string {
		return Sentence("GPGGA," + hms + ".00,4807.038,N,01131.000,E,1,08,0.9,545.4,M,,M,,")
	}
	rmc := func(hms string) string {
		return Sentence("GPRMC," + hms + ".00,A,4807.038,N,01131.000,E,0.0,0.0,181026,,,A")
	}
	vtg := Sentence("GPVTG,0.0,T,,M,0.0,N,0.0,K,A")
	gsv := Sentence("GPGSV,1,1,01,1,2,3,4")
	set := func(hms string) []string { return []string{gga(hms), rmc(hms), vtg} }
	cat := func(ss ...[]string) (all []string) {
		for _, s := range ss {
			all = append(all, s...)
		}
		return
	}
	for _, tc := range []struct {
		name     string
		in, want []string
	}{
		{"one set", set("090507"), set("090507")},
		{"ten per second", cat(set("090507"), set("090507"), set("090507"), set("090508"), set("090508")), cat(set("090507"), set("090508"))},
		{"gsv always", []string{gsv, gsv, gsv}, []string{gsv, gsv, gsv}},
		{"stray vtg before any gga", []string{vtg}, nil},
		{"gga and rmc alone", []string{gga("090507"), gga("090507"), rmc("090507"), rmc("090507")}, []string{gga("090507"), rmc("090507")}},
		{"midnight rollover", cat(set("235959"), set("000000")), cat(set("235959"), set("000000"))},
		{"not a sentence", []string{"junk", ""}, []string{"junk", ""}},
	} {
		s := &NMEAServer{last: map[string]string{}}
		var got []string
		for _, x := range tc.in {
			if s.pass(x) {
				got = append(got, x)
			}
		}
		if strings.Join(got, "") != strings.Join(tc.want, "") {
			t.Errorf("%s: got %d sentences %q, want %d", tc.name, len(got), got, len(tc.want))
		}
	}
}

func readLines(t *testing.T, c net.Conn) <-chan string {
	t.Helper()
	ch := make(chan string, 1024)
	go func() {
		r := bufio.NewReader(c)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				close(ch)
				return
			}
			ch <- l
		}
	}()
	return ch
}

func waitNMEAClients(t *testing.T, s *NMEAServer, n int) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		s.mu.Lock()
		got := len(s.clients)
		s.mu.Unlock()
		if got == n {
			return
		}
	}
	t.Fatalf("no %d clients", n)
}

// Ten DR frames a second, as the module sends them, come out as one valid set a
// second with checksums GeoClue does not check but gpsd does.
func TestNMEAServerRunTCP(t *testing.T) {
	s, err := ListenNMEA("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var cs []net.Conn
	var lines []<-chan string
	for range 2 {
		c, err := net.Dial("tcp", s.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		cs = append(cs, c)
		lines = append(lines, readLines(t, c))
	}
	waitNMEAClients(t, s, 2)

	pr, pw := io.Pipe()
	port := &pipePort{r: pr}
	go func() {
		for sec := byte(7); sec < 10; sec++ {
			u := utcBody(0)
			u[8] = sec
			pw.Write(Encode(u))
			for range 10 {
				pw.Write(Encode(packRawDR(48.1173, 11.5, 0x4000, 1000, 0x80)))
			}
		}
		pw.Close()
	}()
	if st, err := Run(context.Background(), port, RunConfig{Out: teeWriter{io.Discard, s}}); err != nil || st.Sentences != 90 {
		t.Fatalf("%s, %v", st, err)
	}
	s.Close()
	for i, ch := range lines {
		var got []string
		for l := range ch {
			got = append(got, l)
		}
		if len(got) != 9 {
			t.Fatalf("client %d: %d sentences, want 9: %q", i, len(got), got)
		}
		for k, l := range got {
			body := strings.TrimSuffix(l, "\r\n")
			star := strings.LastIndexByte(body, '*')
			if body[0] != '$' || star < 0 || body[star+1:] != strings.ToUpper(body[star+1:]) ||
				Sentence(body[1:star]) != l {
				t.Errorf("client %d line %d: bad framing %q", i, k, l)
			}
			if want := [...]string{"GGA", "RMC", "VTG"}[k%3]; body[3:6] != want {
				t.Errorf("client %d line %d: %s, want %s", i, k, body[3:6], want)
			}
		}
		for k, ts := range []string{",090507.00,", ",090508.00,", ",090509.00,"} {
			if !strings.Contains(got[3*k], ts) || !strings.Contains(got[3*k+1], ts) {
				t.Errorf("client %d: set %d is not at %s: %q", i, k, ts, got[3*k:3*k+2])
			}
		}
		// What GeoClue needs to take a fix (gclue-location.c).
		if f := strings.Split(got[0], ","); len(f) < 11 || f[6] == "0" || f[2] == "" {
			t.Errorf("client %d: GGA would be ignored: %q", i, got[0])
		}
		if f := strings.Split(got[1], ","); len(f) < 12 || f[2] != "A" {
			t.Errorf("client %d: RMC would be ignored: %q", i, got[1])
		}
	}
}

func TestNMEAServerUnix(t *testing.T) {
	p := tapPath(t)
	s, err := ListenNMEA("unix:" + p)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	waitNMEAClients(t, s, 1)
	io.WriteString(s, Sentence("GPGSV,,,,,,,,,,,,,,,,,,,"))
	if l, err := bufio.NewReader(c).ReadString('\n'); err != nil || !strings.HasPrefix(l, "$GPGSV") {
		t.Errorf("%q, %v", l, err)
	}
	if _, err := ListenNMEA("unix:" + p); err == nil {
		t.Error("second listener on a served socket")
	}
	s.Close()
	if _, err := net.Dial("unix", p); err == nil {
		t.Error("socket still served after Close")
	}
}

// A client that never reads loses sentences and does not block Write.
func TestNMEAServerSlowClient(t *testing.T) {
	s, err := ListenNMEA("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitNMEAClients(t, s, 1)
	done := make(chan struct{})
	go func() {
		line := Sentence("GPGSV," + strings.Repeat("x", 1000))
		for range 20000 {
			io.WriteString(s, line)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Write blocked on a client that does not read")
	}
}

func TestNMEAServerBadAddr(t *testing.T) {
	if _, err := ListenNMEA("not an address"); err == nil {
		t.Error("no error for a bad address")
	}
}
