package hippo

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// The helpers pack fields the way the HAL unpacks them (offsets from the
// disassembly), so each test checks the decoder against the HAL's own layout.

func le16(b []byte, i int, v uint16) { binary.LittleEndian.PutUint16(b[i:], v) }
func le32(b []byte, i int, v uint32) { binary.LittleEndian.PutUint32(b[i:], v) }

func degToRaw(d float64) uint32 { return uint32(int32(math.Round(d / degPerLSB))) }

func packRawDR(lat, lon float64, bearingRaw, speedRaw uint16, flags3 byte) []byte {
	b := make([]byte, 0x30)
	b[0], b[1], b[3] = IDRawDR, 2, flags3
	b[2] = 0xfe
	b[4] = 7
	le32(b, 5, 123456)
	le32(b, 9, degToRaw(lat))
	le32(b, 13, degToRaw(lon))
	le16(b, 17, uint16(0xfff6)) // -10
	le16(b, 19, bearingRaw)
	le16(b, 21, speedRaw)
	le16(b, 23, 100)
	le16(b, 25, uint16(0xfffe)) // -2
	le16(b, 29, 15)
	le16(b, 33, 0x4000)
	b[41] = 0x80 | 25
	le32(b, 42, 4000000)
	le16(b, 46, 77)
	return b
}

func TestVersionMatchesTrimbleRelease(t *testing.T) {
	// The package's gps/trimble.txt reads 6.12.7.2023621.
	body := []byte{IDVersion, 1, 6, 12, 7, 21, 6, 0xe7, 0x07}
	v, ok := Parse(body).(*Version)
	if !ok {
		t.Fatalf("%T", Parse(body))
	}
	if v.String() != "6.12.7.2023621" {
		t.Errorf("version %s", v)
	}
	if _, ok := Parse(body[:8]).(*Raw); !ok {
		t.Error("a short version body must stay raw")
	}
}

func TestRawDR(t *testing.T) {
	body := packRawDR(48.1173, -122.5, 0x4000, 1000, 0x80|0x20|0x04)
	r, ok := Parse(body).(*RawDR)
	if !ok {
		t.Fatalf("%T", Parse(body))
	}
	near := func(name string, got, want, eps float64) {
		if math.Abs(got-want) > eps {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	near("lat", r.Lat, 48.1173, 1e-6)
	near("lon", r.Lon, -122.5, 1e-6)
	near("bearing", r.Bearing, 90, 1e-3)
	near("knots", r.Knots(), 19.43844, 1e-3)
	near("m/s", r.MetresPerSecond(), 10, 1e-4)
	near("heading acc", r.HeadingAcc, 90, 1e-3)
	if r.Alt != -10 || r.DeltaDistance != -2 || r.TOW != 123456 || r.GyroCount != 4000000 || r.TachoCount != 77 ||
		r.PosAccuracy != 15 || r.GyroSamples != 25 || !r.DirSwitch || !r.DRSolution() || r.AgeIndex != 7 {
		t.Errorf("%+v", r)
	}
	if got := TimeSource(r.Flags3 >> 4 & 3); got != "GPS(<10ms)" {
		t.Errorf("time source %q", got)
	}
	if _, ok := Parse(body[:0x2f]).(*Raw); !ok {
		t.Error("a short 0x30 body must stay raw")
	}
	body[1] = 1
	if _, ok := Parse(body).(*Raw); !ok {
		t.Error("0x30 sub 1 is not handled by the HAL")
	}
}

func TestGPSFix(t *testing.T) {
	b := make([]byte, 0x28)
	b[0], b[1] = IDGPSFix, 2
	le32(b, 2, 99)
	b[6] = 0x40 | FixValid
	b[7] = 0x20 | 0x0f
	le32(b, 8, degToRaw(-33.9))
	le32(b, 12, degToRaw(151.2))
	le16(b, 16, 58)
	le16(b, 18, 0x8000)
	le16(b, 20, 1234)
	le16(b, 0x1e, 5)
	le32(b, 0x22, uint32(0xffffff9c)) // -100
	le16(b, 0x26, 9)
	g, ok := Parse(b).(*GPSFix)
	if !ok {
		t.Fatalf("%T", Parse(b))
	}
	if g.Status != FixValid || !g.Alt3D || g.DGPS || !g.PosValid || !g.SpeedValid || g.TimeSource != 2 ||
		g.Alt != 58 || g.TOW != 99 || g.VertSpeed != 5 || g.FreqOffset != -100 || g.FreqOffsetAcc != 9 {
		t.Errorf("%+v", g)
	}
	if math.Abs(g.Heading-180) > 1e-3 || math.Abs(g.Speed-12.34) > 1e-4 || math.Abs(g.Lat+33.9) > 1e-6 {
		t.Errorf("heading %v speed %v lat %v", g.Heading, g.Speed, g.Lat)
	}
	if FixStatus(0x11) != "position valid" || FixStatus(0) != "no fix" || !strings.HasPrefix(FixStatus(6), "unknown") {
		t.Error("fix status names")
	}
	if _, ok := Parse(b[:0x27]).(*Raw); !ok {
		t.Error("short 0x31 must stay raw")
	}
	b[1] = 1
	if _, ok := Parse(b).(*Raw); !ok {
		t.Error("0x31 sub 1 is ignored by the HAL")
	}
}

func TestUTC(t *testing.T) {
	b := make([]byte, 0x14)
	b[0], b[1] = IDUTC, 1
	le16(b, 2, 2026)
	b[4], b[5], b[6], b[7], b[8] = 10, 18, 9, 5, 7
	le16(b, 10, 384) // 1.5
	le16(b, 12, 256) // 1.0
	le16(b, 14, 640) // 2.5
	b[0x11] = 3 << 4
	b[0x13] = 9
	u, ok := Parse(b).(*UTC)
	if !ok {
		t.Fatalf("%T", Parse(b))
	}
	if u.Year != 2026 || u.Month != 10 || u.Day != 18 || u.Hour != 9 || u.Min != 5 || u.Sec != 7 ||
		u.PDOP != 1.5 || u.HDOP != 1 || u.VDOP != 2.5 || u.Visible != 9 || TimeSource(u.TimeSource) != "GPS(<1ms)" {
		t.Errorf("%+v", u)
	}
}

func TestChannel(t *testing.T) {
	b := []byte{IDChannel, 1, 0xe5, 17, 0x33, 200, 90, 45, 0x0d}
	c, ok := Parse(b).(*Channel)
	if !ok {
		t.Fatalf("%T", Parse(b))
	}
	if c.Index != 5 || c.PRN != 17 || c.SNR != 40 || c.Azimuth != 180 || c.Elevation != 45 || c.Extra != 0x0d || c.Flags != 0x33 {
		t.Errorf("%+v", c)
	}
}

func TestSmallReplies(t *testing.T) {
	if p, ok := Parse([]byte{IDProtocol, 2, 5, 0, 3}).(*Protocol); !ok || p.Mask != 3 {
		t.Errorf("protocol reply: %v", Parse([]byte{IDProtocol, 2, 5, 0, 3}))
	}
	if d, ok := Parse([]byte{IDDRControl, 1, 0, 0, 0x20, 0x41}).(*DRRate); !ok || d.Rate != 0x2041 {
		t.Errorf("dr rate: %v", Parse([]byte{IDDRControl, 1, 0, 0, 0x20, 0x41}))
	}
	if c, ok := Parse([]byte{IDDRControl, 2, 4, 1, 0, 0, 0, 0xff}).(*Constellation); !ok || c.Version != 4 || c.Mask != 0x01ff {
		t.Errorf("constellation: %v", Parse([]byte{IDDRControl, 2, 4, 1, 0, 0, 0, 0xff}))
	}
	l := []byte{IDLog, 1, 0, 0xea, 0x07, 6, 21, 12, 30, 45, 0x75, 0x02}
	if e, ok := Parse(l).(*LogEvent); !ok || e.Year != 2026 || e.EventCode != 0x75 || e.StatusCode != 2 || e.Sec != 45 {
		t.Errorf("log: %v", Parse(l))
	}
}

func TestExt70(t *testing.T) {
	b := make([]byte, 0x58)
	b[0], b[1], b[3] = IDExt, 0x22, 4
	le32(b, 30, uint32(math.Float64bits(87.25)))
	le32(b, 34, uint32(math.Float64bits(87.25)>>32))
	le32(b, 38, uint32(math.Float64bits(50.5)))
	le32(b, 42, uint32(math.Float64bits(50.5)>>32))
	e, ok := Parse(b).(*ExtGPSFix)
	if !ok || e.AltHAE != 87.25 || e.AltMSL != 50.5 || e.Status != 4 {
		t.Errorf("%v", Parse(b))
	}
	u, ok := Parse([]byte{IDExt, 0x24, 0, 3, 5, 9, 70, 0xaa}).(*ExtUsedSV)
	if !ok || u.Count != 3 || len(u.IDs) != 3 || u.IDs[2] != 70 {
		t.Errorf("%v", Parse([]byte{IDExt, 0x24, 0, 3, 5, 9, 70, 0xaa}))
	}
	if x, ok := Parse([]byte{IDExt, 0x30, 1, 2}).(*Ext70); !ok || !strings.Contains(x.Name, "DRKF") {
		t.Errorf("%v", Parse([]byte{IDExt, 0x30, 1, 2}))
	}
}

func TestUnknownIsRaw(t *testing.T) {
	for _, b := range [][]byte{{0x99, 1, 2}, {IDExt, 0x77}, {IDVersion}, {IDProtocol, 1, 0, 0, 0}} {
		m := Parse(b)
		if r, ok := m.(*Raw); !ok || !strings.Contains(m.Describe(), Hex(b)) {
			t.Errorf("% x: %T %s", b, m, m.Describe())
		} else if r.Note == "" {
			t.Errorf("% x: no note", b)
		}
	}
}
