package hippo

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMainUsage(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
		err  string
	}{
		{"no device", nil, 2, "-dev"},
		{"unknown flag", []string{"-nope"}, 2, "flag provided but not defined"},
		{"help", []string{"-h"}, 0, "-replay-stock"},
	} {
		var out, errb bytes.Buffer
		if code := Main(tc.args, &out, &errb); code != tc.code {
			t.Errorf("%s: exit %d, want %d", tc.name, code, tc.code)
		}
		if !strings.Contains(errb.String(), tc.err) {
			t.Errorf("%s: stderr %q lacks %q", tc.name, errb.String(), tc.err)
		}
		if out.Len() != 0 {
			t.Errorf("%s: wrote %q to stdout", tc.name, out.String())
		}
	}
}

func TestMainNoSuchDevice(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{"-dev", "/dev/does-not-exist"}, &out, &errb); code != 1 {
		t.Errorf("exit %d, want 1; stderr %q", code, errb.String())
	}
}

// pipePort is a port whose reads come from a test and whose writes are kept.
type pipePort struct {
	r  *io.PipeReader
	mu sync.Mutex
	w  bytes.Buffer
}

func (p *pipePort) Read(b []byte) (int, error) { return p.r.Read(b) }
func (p *pipePort) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.w.Write(b)
}
func (p *pipePort) Close() error { return p.r.Close() }
func (p *pipePort) written() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return hexs(p.w.Bytes())
}

func hexs(b []byte) string { return Hex(b) }

func TestRunConvertsAndDumps(t *testing.T) {
	pr, pw := io.Pipe()
	port := &pipePort{r: pr}
	var out, dump bytes.Buffer
	go func() {
		pw.Write([]byte("garbage"))
		pw.Write(Encode(utcBody(0)))
		pw.Write(Encode([]byte{0x99, 1, 2, 3}))
		pw.Write(Encode(packRawDR(48.1173, 11.5, 0x4000, 1000, 0x80)))
		bad := Encode([]byte{IDVersion, 1})
		bad[len(bad)-2] ^= 1
		pw.Write(bad)
		pw.Close()
	}()
	st, err := Run(context.Background(), port, RunConfig{Out: &out, Dump: &dump})
	if err != nil {
		t.Fatal(err)
	}
	if st.Frames != 4 || st.BadSum != 1 || st.Sentences != 3 || st.Garbage != len("garbage") {
		t.Errorf("%s", st)
	}
	if n := strings.Count(out.String(), "\r\n"); n != 3 || !strings.HasPrefix(out.String(), "$GPGGA,090507.00,4807.0") {
		t.Errorf("output %q", out.String())
	}
	d := dump.String()
	for _, want := range []string{"# utc 2026-10-18", "# raw id 0x99 sub 0x01 not handled  99 01 02 03", "# raw DR", "BAD csum"} {
		if !strings.Contains(d, want) {
			t.Errorf("dump lacks %q:\n%s", want, d)
		}
	}
}

func TestRunQuerySendsOnlyQueries(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	port := &pipePort{r: pr}
	var logs bytes.Buffer
	_, err := Run(context.Background(), port, RunConfig{
		Send: SafeQueries(), Gap: time.Millisecond, Wait: 50 * time.Millisecond,
		Logf: func(f string, a ...any) { logs.WriteString(f) },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "81 02 11 01 e9 82" + "81 02 21 01 d9 82" + "81 02 21 02 d8 82" + "81 02 23 02 d6 82"
	if got := strings.ReplaceAll(port.written(), " ", ""); got != strings.ReplaceAll(want, " ", "") {
		t.Errorf("sent %s, want %s", got, want)
	}
}

func TestRunStopsOnContext(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	port := &pipePort{r: pr}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { Run(ctx, port, RunConfig{}); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run ignored its context")
	}
}

func TestOpenOutFile(t *testing.T) {
	path := t.TempDir() + "/nmea.txt"
	w, closeOut, err := openOut("file:"+path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, "$X*00\r\n")
	closeOut()
	if _, _, err := openOut("-", io.Discard); err != nil {
		t.Error(err)
	}
}
