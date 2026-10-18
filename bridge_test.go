package hippo

import (
	"strings"
	"testing"
)

func feed(c *Converter, bodies ...[]byte) []string {
	var out []string
	for _, b := range bodies {
		out = append(out, c.Feed(Parse(b))...)
	}
	return out
}

func pick(out []string, talker string) []string {
	for _, s := range out {
		if strings.HasPrefix(s, "$"+talker) {
			return strings.Split(strings.SplitN(s[1:], "*", 2)[0], ",")
		}
	}
	return nil
}

func utcBody(visible byte) []byte {
	b := make([]byte, 0x14)
	b[0], b[1] = IDUTC, 1
	le16(b, 2, 2026)
	b[4], b[5], b[6], b[7], b[8] = 10, 18, 9, 5, 7
	le16(b, 12, 384)
	b[0x13] = visible
	return b
}

func fixBody(status byte) []byte {
	b := make([]byte, 0x28)
	b[0], b[1], b[6] = IDGPSFix, 2, status
	return b
}

func TestConverterNeedsTime(t *testing.T) {
	var c Converter
	if got := feed(&c, packRawDR(1, 2, 0, 0, 0)); got != nil {
		t.Errorf("sentences before any UTC message: %q", got)
	}
}

func TestConverterFix(t *testing.T) {
	var c Converter
	ext := make([]byte, 0x58)
	ext[0], ext[1] = IDExt, 0x22
	le32(ext, 42, 0x40490000) // alt_msl 50.0
	le32(ext, 34, 0x40554000) // alt_hae 85.0
	used := []byte{IDExt, 0x24, 0, 7, 1, 2, 3, 4, 5, 6, 7}
	out := feed(&c, utcBody(9), ext, used, fixBody(FixValid),
		packRawDR(48.1173, 11.516666666, 0x4000, 1000, 0x00))
	if len(out) != 6 {
		t.Fatalf("%d sentences: %q", len(out), out)
	}
	for _, s := range out {
		checkSentence(t, s)
	}
	gga := pick(out, "GPGGA")
	// The position is quantised to 180/2^31 degrees by the wire format.
	if got := strings.Join(gga, ","); !strings.HasPrefix(got, "GPGGA,090507.00,4807.038") || !strings.HasSuffix(got, ",1,07,1.5,50.0,M,35.0,M,,") {
		t.Errorf("GGA %q", got)
	}
	rmc := pick(out, "GPRMC")
	if got := strings.Join(rmc, ","); !strings.HasSuffix(got, ",19.4,90.0,181026,,,A") || rmc[2] != "A" || rmc[3][:8] != "4807.038" {
		t.Errorf("RMC %q", got)
	}
	if v := pick(out, "GPVTG"); v == nil || v[1] != "90.0" {
		t.Errorf("VTG %v", v)
	}
	if g := pick(out, "GPGSV"); g == nil || g[3] != "9" {
		t.Errorf("GSV %v", g)
	}
}

func TestConverterDeadReckoningAndNoFix(t *testing.T) {
	var c Converter
	out := feed(&c, utcBody(0), packRawDR(10, 20, 0, 0, 0x80))
	if g := checkSentence(t, out[0]); g[6] != "6" || g[3] == "" {
		t.Errorf("DR solution flag must give quality 6 with a position: %v", g)
	}
	if r := checkSentence(t, out[1]); r[len(r)-1] != "E" || r[2] != "A" {
		t.Errorf("RMC %v", r)
	}
	var n Converter
	out = feed(&n, utcBody(0), packRawDR(10, 20, 0, 0, 0))
	if g := checkSentence(t, out[0]); g[6] != "0" || g[2] != "" || g[3] != "" {
		t.Errorf("no fix: quality 0 and no position, got %v", g)
	}
	if r := checkSentence(t, out[1]); r[2] != "V" || r[len(r)-1] != "N" {
		t.Errorf("RMC %v", r)
	}
}

func TestConverterAltFromDR(t *testing.T) {
	c := Converter{AltFromDR: true}
	raw := packRawDR(1, 2, 0, 0, 0)
	raw[2] = 0x02
	le16(raw, 17, 123)
	out := feed(&c, utcBody(0), fixBody(FixValid), raw)
	if g := pick(out, "GPGGA"); g[9] != "123.0" {
		t.Errorf("alt %v", g)
	}
	var d Converter
	out = feed(&d, utcBody(0), fixBody(FixValid), raw)
	if g := pick(out, "GPGGA"); g[9] != "" {
		t.Errorf("alt without -alt-dr must be empty, got %v", g)
	}
}

// The 0x31 altitude (int16 metres) is the default when it is valid, with no
// separation; an invalid one, or none, leaves the field empty.
func TestConverterAltFromFix(t *testing.T) {
	var c Converter
	fix := fixBody(FixValid)
	fix[7] = 1 << 1 // altitude valid
	le16(fix, 16, 293)
	out := feed(&c, utcBody(0), fix, packRawDR(1, 2, 0, 0, 0))
	if g := pick(out, "GPGGA"); g[9] != "293.0" || g[10] != "M" || g[11] != "" || g[12] != "M" {
		t.Errorf("altitude fields %q", g[9:])
	}
	var d Converter
	fix[7] = 0
	out = feed(&d, utcBody(0), fix, packRawDR(1, 2, 0, 0, 0))
	if g := pick(out, "GPGGA"); g[9] != "" {
		t.Errorf("an invalid altitude must leave the field empty, got %v", g)
	}
}

func TestConverterGSV(t *testing.T) {
	var c Converter
	chans := [][]byte{
		{IDChannel, 1, 0, 5, 0x11, 150, 100, 30, 0},
		{IDChannel, 1, 1, 9, 0x11, 100, 20, 60, 0},
	}
	out := feed(&c, utcBody(2), chans[0], chans[1], fixBody(FixValid))
	if len(out) != 1 {
		t.Fatalf("%q", out)
	}
	if g := checkSentence(t, out[0]); strings.Join(g, ",") != "GPGSV,1,1,2,5,30,200,30,9,60,40,20" {
		t.Errorf("GSV %q", strings.Join(g, ","))
	}
	var e Converter
	if out := feed(&e, utcBody(0), fixBody(FixValid)); len(out) != 1 || out[0] != "$GPGSV,,,,,,,,,,,,,,,,,,,*79\r\n" {
		t.Errorf("empty GSV %q", out)
	}
}
