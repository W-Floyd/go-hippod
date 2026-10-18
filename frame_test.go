package hippo

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The nine frames of the findings, with the checksums worked out by hand from
// the HAL's calc_csum before any code existed.
var golden = []struct{ name, frame string }{
	{"reset", "81 03 01 f9 82"},
	{"query-version", "81 02 11 01 e9 82"},
	{"query-dr-rate", "81 02 21 01 d9 82"},
	{"query-constellation", "81 02 21 02 d8 82"},
	{"query-protocol", "81 02 23 02 d6 82"},
	{"poll-70-50", "81 02 70 50 3b 82"},
	{"poll-70-51", "81 02 70 51 3a 82"},
	{"set-protocol-3", "81 01 23 02 05 00 03 cf 82"},
	{"set-nmea-control", "81 01 22 02 00 09 01 00 00 ce 82"},
}

func TestStockGolden(t *testing.T) {
	if len(golden) != len(Stock) {
		t.Fatalf("%d golden frames, %d stock frames", len(golden), len(Stock))
	}
	for _, g := range golden {
		s, ok := StockByName(g.name)
		if !ok {
			t.Errorf("%s: not in Stock", g.name)
			continue
		}
		if got, want := s.Frame(), unhex(t, g.frame); !bytes.Equal(got, want) {
			t.Errorf("%s: % x, want % x", g.name, got, want)
		}
	}
}

func TestSafeQueriesAreOnlyQueries(t *testing.T) {
	var names []string
	for _, s := range SafeQueries() {
		if !s.Query || s.Body[0] != ClassQuery {
			t.Errorf("%s is not a query", s.Name)
		}
		names = append(names, s.Name)
	}
	if got := strings.Join(names, " "); got != "query-version query-dr-rate query-constellation query-protocol" {
		t.Errorf("safe queries: %s", got)
	}
	for _, s := range ReplayStock() {
		if s.Query || s.Safe {
			t.Errorf("%s must not be marked safe", s.Name)
		}
	}
}

func TestSetProtocolMask(t *testing.T) {
	for m, sum := range map[byte]byte{1: 0xd1, 2: 0xd0, 3: 0xcf} {
		f := Encode(SetProtocolMask(m))
		if f[len(f)-2] != sum {
			t.Errorf("mask %d: checksum %02x, want %02x", m, f[len(f)-2], sum)
		}
	}
}

func TestEscapes(t *testing.T) {
	// Find bodies whose checksum is itself 0x80, 0x81 or 0x82.
	var special [][]byte
	for _, want := range []byte{0x80, 0x81, 0x82} {
		for b := 0; b < 256; b++ {
			if body := []byte{0x02, byte(b)}; Sum(body) == want {
				special = append(special, body)
				break
			}
		}
	}
	bodies := append([][]byte{
		{0x80}, {0x81}, {0x82}, {0x80, 0x81, 0x82, 0x00, 0x7f, 0xff}, {0x01, 0x80, 0x80},
	}, special...)
	for _, body := range bodies {
		wire := Encode(body)
		if bytes.Contains(wire[1:len(wire)-1], []byte{SOF}) || bytes.Contains(wire[1:len(wire)-1], []byte{EOF}) {
			t.Errorf("% x: delimiter inside the frame % x", body, wire)
		}
		var d Decoder
		fr := d.Write(wire)
		if len(fr) != 1 || !fr[0].Valid || !bytes.Equal(fr[0].Body, body) {
			t.Errorf("% x: round trip gave %v", body, fr)
		}
	}
	if len(special) != 3 {
		t.Errorf("found %d bodies with a delimiter-valued checksum, want 3", len(special))
	}
	if w := Encode([]byte{0x80, 0x81, 0x82}); !bytes.HasPrefix(w, unhex(t, "81 80 00 80 01 80 02")) {
		t.Errorf("escape bytes: % x", w)
	}
}

func TestDecoderStreamsByteByByte(t *testing.T) {
	var wire []byte
	for _, g := range golden {
		wire = append(wire, unhex(t, g.frame)...)
	}
	var d Decoder
	var n int
	for _, b := range wire {
		for _, f := range d.Write([]byte{b}) {
			if !f.Valid {
				t.Errorf("frame %d invalid: %v", n, f)
			}
			n++
		}
	}
	if n != len(golden) || d.Garbage() != 0 || d.Dropped() != 0 {
		t.Errorf("%d frames, garbage %d, dropped %d", n, d.Garbage(), d.Dropped())
	}
}

func TestDecoderResync(t *testing.T) {
	good := Encode([]byte{0x23, 0x02, 0x05, 0x00, 0x03})
	var wire []byte
	wire = append(wire, "$GPGGA noise\r\n"...)              // outside a frame
	wire = append(wire, 0x81, 0x11, 0x01)                   // frame cut short by the next start
	wire = append(wire, good...)                            // restart: only this one survives
	wire = append(wire, 0x81, 0x01, 0x80, 0x09)             // bad escape (> 7)
	wire = append(wire, 0x82, 0x82)                         // stray ends
	wire = append(wire, 0x81, 0x82)                         // empty frame
	wire = append(wire, 0x81)                               // too long below
	wire = append(wire, bytes.Repeat([]byte{0x11}, 300)...) // overflow
	wire = append(wire, 0x82)
	bad := Encode([]byte{0x23, 0x02})
	bad[len(bad)-2] ^= 0x40 // corrupt checksum
	wire = append(wire, bad...)
	wire = append(wire, good...)

	var d Decoder
	fr := d.Write(wire)
	// The restart drops the cut-short frame, so what comes out is the good
	// frame, the corrupt one (reported, not hidden) and the last good one.
	if len(fr) != 3 {
		t.Fatalf("%d frames, want 3: %v", len(fr), fr)
	}
	want := []bool{true, false, true}
	for i, f := range fr {
		if f.Valid != want[i] {
			t.Errorf("frame %d: valid=%v, want %v (%v)", i, f.Valid, want[i], f)
		}
	}
	if !bytes.Equal(fr[0].Body, []byte{0x23, 0x02, 0x05, 0x00, 0x03}) {
		t.Errorf("first frame %v", fr[0])
	}
	// Dropped: cut-short, bad escape, empty frame, overflow.
	if d.Garbage() == 0 || d.Dropped() != 4 {
		t.Errorf("garbage %d dropped %d, want >0 and 4", d.Garbage(), d.Dropped())
	}
}
