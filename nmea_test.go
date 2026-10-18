package hippo

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// checkSentence verifies "$...*HH\r\n" and returns its comma-split body.
func checkSentence(t testing.TB, s string) []string {
	t.Helper()
	if !strings.HasPrefix(s, "$") || !strings.HasSuffix(s, "\r\n") {
		t.Fatalf("bad framing %q", s)
	}
	body, sum, ok := strings.Cut(s[1:len(s)-2], "*")
	if !ok || len(sum) != 2 {
		t.Fatalf("no checksum in %q", s)
	}
	n, err := strconv.ParseUint(sum, 16, 8)
	if err != nil || byte(n) != NMEAChecksum(body) || sum != strings.ToUpper(sum) {
		t.Fatalf("checksum %s in %q, want %02X", sum, s, NMEAChecksum(body))
	}
	return strings.Split(body, ",")
}

func TestNMEAChecksumKnown(t *testing.T) {
	// The classic example sentence from the NMEA 0183 literature.
	if got := NMEAChecksum("GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,"); got != 0x47 {
		t.Errorf("%02X, want 47", got)
	}
	// The HAL's fixed empty GSV carries *79 (0xb927).
	if got := Sentence(emptyGSV); got != "$GPGSV,,,,,,,,,,,,,,,,,,,*79\r\n" {
		t.Errorf("empty GSV %q", got)
	}
}

func TestCoord(t *testing.T) {
	for _, c := range []struct {
		deg   float64
		width int
		want  string
		h     byte
	}{
		{48.1173, 4, "4807.038000", 'N'},
		{-48.1173, 4, "4807.038000", 'S'},
		{11.516666666, 5, "01131.000000", 'E'},
		{-122.5, 5, "12230.000000", 'W'},
		{10.999999999, 4, "1100.000000", 'N'}, // carry: 59.99999994 min rounds up
		{0, 4, "0000.000000", 'N'},
	} {
		got, h := coord(c.deg, c.width, 'N', 'S')
		if c.width == 5 {
			got, h = coord(c.deg, c.width, 'E', 'W')
		}
		if got != c.want || h != c.h {
			t.Errorf("coord(%v) = %s,%c want %s,%c", c.deg, got, h, c.want, c.h)
		}
	}
}

func TestGGAandRMCFields(t *testing.T) {
	g := checkSentence(t, GGA(GGAData{Hour: 9, Min: 5, Sec: 7, HasPos: true, Lat: 48.1173, Lon: 11.516666666,
		Quality: 1, Sats: 8, HDOP: 0.94, HasAlt: true, AltMSL: 545.44, Sep: 46.9, HasSep: true}))
	want := "GPGGA 090507.00 4807.038000 N 01131.000000 E 1 08 0.9 545.4 M 46.9 M  "
	if got := strings.Join(g, " "); got != want {
		t.Errorf("GGA\n got %q\nwant %q", got, want)
	}
	g = checkSentence(t, GGA(GGAData{Hour: 1, HDOP: 1}))
	if strings.Join(g, ",") != "GPGGA,010000.00,,,,,0,00,1.0,,M,,M,," {
		t.Errorf("no-fix GGA %q", strings.Join(g, ","))
	}
	r := checkSentence(t, RMC(RMCData{Hour: 9, Min: 5, Sec: 7, Day: 18, Month: 10, Year: 2026, Valid: true, HasPos: true,
		Lat: -33.9, Lon: 151.2, Knots: 5.55, Course: 359.96, Mode: 'A'}))
	if got := strings.Join(r, ","); got != "GPRMC,090507.00,A,3354.000000,S,15112.000000,E,5.5,360.0,181026,,,A" {
		t.Errorf("RMC %q", got)
	}
	v := checkSentence(t, VTG(90, 10, 'A'))
	if got := strings.Join(v, ","); got != "GPVTG,90.0,T,,M,10.0,N,18.5,K,A" {
		t.Errorf("VTG %q", got)
	}
}

func TestGSV(t *testing.T) {
	var sats []SatView
	for i := 0; i < 10; i++ {
		sats = append(sats, SatView{PRN: i + 1, Elev: 10 + i, Az: 20 * i, SNR: 30 + i, Known: i != 5})
	}
	out := GSV(sats, 10)
	if len(out) != 3 {
		t.Fatalf("%d sentences, want 3", len(out))
	}
	first := checkSentence(t, out[0])
	if strings.Join(first, ",") != "GPGSV,3,1,10,1,10,0,30,2,11,20,31,3,12,40,32,4,13,60,33" {
		t.Errorf("first %v", first)
	}
	if last := checkSentence(t, out[2]); strings.Join(last, ",") != "GPGSV,3,3,10,9,18,160,38,10,19,180,39" {
		t.Errorf("last %v", last)
	}
	if mid := checkSentence(t, out[1]); !strings.Contains(strings.Join(mid, ","), ",5,14,80,34,,,,,7,") {
		// the unknown satellite (index 5) prints empty fields
		t.Errorf("mid %v", mid)
	}
	if got := GSV(nil, 0); len(got) != 1 || got[0] != "$GPGSV,,,,,,,,,,,,,,,,,,,*79\r\n" {
		t.Errorf("empty %q", got)
	}
	// The HAL caps at six sentences.
	if got := GSV(make([]SatView, 30), 30); len(got) != 6 {
		t.Errorf("%d sentences for 30 sats, want 6", len(got))
	}
	for _, s := range GSV(make([]SatView, 5), 5) {
		checkSentence(t, s)
	}
}

func TestSentencesShortEnough(t *testing.T) {
	// NMEA 0183 limits a sentence to 82 characters including $ and CRLF.
	for _, s := range GSV([]SatView{{PRN: 32, Elev: 90, Az: 359, SNR: 99, Known: true}, {PRN: 32, Elev: 90, Az: 359, SNR: 99, Known: true},
		{PRN: 32, Elev: 90, Az: 359, SNR: 99, Known: true}, {PRN: 32, Elev: 90, Az: 359, SNR: 99, Known: true}}, 4) {
		if len(s) > 82 {
			t.Errorf("%d chars: %q", len(s), s)
		}
	}
	_ = fmt.Sprint
}
