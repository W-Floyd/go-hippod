package hippo

import (
	"fmt"
	"math"
	"strings"
)

// Scale factors the HAL multiplies raw fields by, as the constants it loads.
// Each is read from the library's literal pool (addresses in the comments).
var (
	// degPerLSB is 180/2^31, applied to the signed 32-bit latitude and
	// longitude (RawDR 0x2090, GPSFix 0x2760): 0x3e76800001482124.
	degPerLSB = math.Float64frombits(0x3e76800001482124)
	// bearingPerLSB is 360/65536 for 16-bit headings (0x2098, 0x2768):
	// 0x3f767ffffbb47d06.
	bearingPerLSB = math.Float64frombits(0x3f767ffffbb47d06)
	// knotsPerLSB turns the RawDR speed into knots (0x20a0, a float32).
	knotsPerLSB = float32(0.01943844)
	// dopPerLSB is 1/256 for the three DOPs of the UTC message (0x42d8).
	dopPerLSB = math.Float64frombits(0x3f70000000000000)
	// snrPerLSB is 0.2 for the channel SNR (0x4768): 0x3fc999999999999a.
	snrPerLSB = math.Float64frombits(0x3fc999999999999a)
)

// Message is a decoded frame body.
type Message interface {
	// Describe is one line with the decoded fields.
	Describe() string
}

// Raw is a body that is not decoded: an unknown id, a known id with a
// sub-type the HAL does not handle, or a body shorter than the HAL's own
// length check requires. Note says which.
type Raw struct {
	Body []byte
	Note string
}

func (r *Raw) Describe() string {
	return fmt.Sprintf("raw %s  % x", r.Note, r.Body)
}

func u16(b []byte, i int) uint16 { return uint16(b[i]) | uint16(b[i+1])<<8 }
func s16(b []byte, i int) int16  { return int16(u16(b, i)) }
func u32(b []byte, i int) uint32 { return uint32(u16(b, i)) | uint32(u16(b, i+2))<<16 }
func s32(b []byte, i int) int32  { return int32(u32(b, i)) }
func be16(b []byte, i int) uint16 {
	return uint16(b[i])<<8 | uint16(b[i+1])
}

func f64le(b []byte, i int) float64 {
	return math.Float64frombits(uint64(u32(b, i)) | uint64(u32(b, i+4))<<32)
}

func bit(v byte, n uint) bool { return v>>n&1 != 0 }

// TimeSource names the 2-bit time source field of RawDR, GPSFix and UTC
// (RawDR 0x19f4-0x1b7c, GPSFix 0x2e6e-0x2ec8).
func TimeSource(v byte) string {
	return [...]string{"system clock", "RTC", "GPS(<10ms)", "GPS(<1ms)"}[v&3]
}

// FixStatus names the 6-bit fix status of GPSFix: the HAL's jump table at
// 0x299c (values 2, 6 and 7 fall to its "default value" branch).
func FixStatus(v byte) string {
	switch v {
	case 0:
		return "no fix"
	case 1:
		return "input position, approximate"
	case 3:
		return "input position, accurate"
	case 4:
		return "have internal position"
	case 5:
		return "KF position accuracy too large"
	case 8:
		return "old valid GPS fix"
	case 9, 10, 11:
		return "converging"
	case 12:
		return "output fix criterion failed"
	case 13:
		return "2-D fix, no reference altitude"
	case 14:
		return "KF velocity RAIM failed"
	case 15:
		return "KF edited too many SVs"
	case 16:
		return "KF position RAIM failed"
	case 0x11:
		return "position valid"
	}
	return fmt.Sprintf("unknown(%d)", v)
}

// FixValid is the fix status the HAL treats as a valid fix (0x2744, 0x7e42).
const FixValid = 0x11

// Version is message 0x11 sub 1, "processVersionInfo" (0x3c92-0x3d84). The
// HAL's version string "%d.%d.%d.%d%d%d" (major, minor, build, year, month,
// day) reproduces the Trimble release name, e.g. 6.12.7.2023621.
type Version struct {
	Major, Minor, Build byte
	Year                uint16
	Month, Day          byte
}

func (v *Version) String() string {
	return fmt.Sprintf("%d.%d.%d.%d%d%d", v.Major, v.Minor, v.Build, v.Year, v.Month, v.Day)
}
func (v *Version) Describe() string {
	return fmt.Sprintf("version %s  (%04d-%02d-%02d)", v, v.Year, v.Month, v.Day)
}

// LogEvent is message 0x14 sub 1, "processLogMsg" SOFT_EVENT_LOG (0x3d8c-0x3e98).
// Event codes 0x74-0x77 and 0x80-0x82 are forwarded to the HAL's status
// callback with different ids; their meaning is not in the library.
type LogEvent struct {
	Year                       uint16
	Month, Day, Hour, Min, Sec byte
	EventCode, StatusCode      byte
}

func (l *LogEvent) Describe() string {
	return fmt.Sprintf("soft event log %04d-%02d-%02d %02d:%02d:%02d event 0x%02x status 0x%02x",
		l.Year, l.Month, l.Day, l.Hour, l.Min, l.Sec, l.EventCode, l.StatusCode)
}

// FatalLog is message 0x14 sub 2, FATAL_ERROR_LOG (0x3e9c-0x3f04); the HAL
// only reports it, so the body is not decoded.
type FatalLog struct{ Body []byte }

func (f *FatalLog) Describe() string { return fmt.Sprintf("FATAL_ERROR_LOG  % x", f.Body) }

// DRRate is message 0x21 sub 1, the reply to the DR engine rate query
// (0x54ac-0x5526): Rate = b[4]<<8|b[5] (stored by the HAL as dr_engine_rate).
type DRRate struct {
	Rate uint16
	Body []byte
}

func (d *DRRate) Describe() string { return fmt.Sprintf("dr engine rate 0x%04x", d.Rate) }

// Constellation is message 0x21 sub 2, "processConstellation" (0x5528-0x5690).
// Version is b[2] and Mask is b[3]<<8|b[7], as the HAL packs them for its log
// "version 0x%x, mask 0x%x"; the bit meanings and b[4:7] are unknown.
type Constellation struct {
	Version byte
	Mask    uint16
	Body    []byte
}

func (c *Constellation) Describe() string {
	return fmt.Sprintf("constellation version 0x%x mask 0x%04x  raw % x", c.Version, c.Mask, c.Body)
}

// Protocol is message 0x23 sub 2, the reply to the output protocol query and
// to the set (0x57a0-0x57d6): Mask is b[4]. The mask bits' meaning is unknown;
// the HAL insists on 3.
type Protocol struct {
	Mask byte
	Body []byte
}

func (p *Protocol) Describe() string {
	return fmt.Sprintf("output protocol mask %d (0x%02x)  raw % x", p.Mask, p.Mask, p.Body)
}

// RawDR is message 0x30 sub 2, "processRawDr" (0x1710-0x23b8, body >= 0x30
// bytes): the dead-reckoning solution, which the HAL turns into $GPGGA and
// $GPRMC. Flags2 and Flags3 are kept as bytes; the bit names are the HAL's
// log texts.
//
//	Flags2: b1 altitude status, b2 heading status, b3 speed status,
//	        b4 direction switch, b5 delta-distance status, b6 delta-heading
//	        status, b7 motion status (0x179e-0x18e4)
//	Flags3: b0 motion indicator, b1 direction (backward), b2 gyro
//	        calibration, b3 tacho calibration, b4-5 time source, b6 snap to
//	        DR-GPS, b7 DR solution status (0x1914-0x1d2a)
type RawDR struct {
	Flags2, Flags3                   byte
	AgeIndex                         byte // "GPS Data Age Index", b[4]
	TOW                              uint32
	Lat, Lon                         float64 // degrees
	Alt                              int16   // "Altitude", b[17:19], unscaled; unit not stated
	Bearing                          float64 // degrees
	SpeedRaw                         uint16  // b[21:23]: *0.01943844 = knots, /100 = m/s
	DeltaTime                        uint16
	DeltaDistance                    int16
	DeltaBearing                     int16
	PosAccuracy                      uint16
	AltAccuracy                      uint16
	HeadingAcc                       float64 // degrees
	SpeedAccuracy                    uint16
	GPSDeltaDistAcc, GPSDeltaHeadAcc uint16
	GyroSamples                      byte // b[0x29] low 7 bits
	DirSwitch                        bool // b[0x29] bit 7
	GyroCount                        uint32
	TachoCount                       uint16
}

// DRSolution is Flags3 bit 7, "DR Solution Status" (0x1d02).
func (r *RawDR) DRSolution() bool { return bit(r.Flags3, 7) }

// Knots is the speed in knots, computed in float32 like the HAL (0x1ef6-0x1efa).
func (r *RawDR) Knots() float64 { return float64(float32(r.SpeedRaw) * knotsPerLSB) }

// MetresPerSecond is the speed in m/s (raw/100, 0x1f08).
func (r *RawDR) MetresPerSecond() float64 { return float64(float32(r.SpeedRaw) / 100) }

func (r *RawDR) Describe() string {
	return fmt.Sprintf("raw DR  lat %.7f lon %.7f alt %d bearing %.2f speed %.2fkn tow %d  flags2 %08b flags3 %08b time=%s dr=%v  accuracy pos %d alt %d head %.2f speed %d  gyro n=%d cnt=%d tacho %d",
		r.Lat, r.Lon, r.Alt, r.Bearing, r.Knots(), r.TOW, r.Flags2, r.Flags3,
		TimeSource(r.Flags3>>4&3), r.DRSolution(), r.PosAccuracy, r.AltAccuracy, r.HeadingAcc, r.SpeedAccuracy,
		r.GyroSamples, r.GyroCount, r.TachoCount)
}

// GPSFix is message 0x31 sub 2, "processGPSFix" (0x24a8-0x2fba, body >= 0x28
// bytes), the GNSS fix; its Status is what the HAL calls a fix.
type GPSFix struct {
	TOW                                       uint32
	Status                                    byte // b[6] & 0x3f, see FixStatus
	Alt3D                                     bool // b[6] bit 6: full 3-D fix (else altitude hold)
	DGPS                                      bool // b[6] bit 7, logged "DGPS status"
	PosValid, AltValid, HeadValid, SpeedValid bool // b[7] bits 0-3 (0x2938-0x2e68)
	TimeSource                                byte // b[7] bits 4-5
	Lat, Lon                                  float64
	Alt                                       int16
	Heading                                   float64 // degrees
	Speed                                     float64 // m/s, u16/100
	Unk22                                     int16   // b[22:24], stored by the HAL, logged with a reused label
	VertSpeed, VertSpeedAcc                   uint16  // b[0x1e:0x20], b[0x20:0x22]
	FreqOffset                                int32   // b[0x22:0x26]
	FreqOffsetAcc                             uint16  // b[0x26:0x28]
}

func (g *GPSFix) Describe() string {
	return fmt.Sprintf("gnss fix  status %d (%s) 3d=%v dgps=%v valid pos/alt/head/speed %v/%v/%v/%v time=%s  lat %.7f lon %.7f alt %d heading %.2f speed %.2fm/s tow %d  vspeed %d±%d freq %d±%d",
		g.Status, FixStatus(g.Status), g.Alt3D, g.DGPS, g.PosValid, g.AltValid, g.HeadValid, g.SpeedValid,
		TimeSource(g.TimeSource), g.Lat, g.Lon, g.Alt, g.Heading, g.Speed, g.TOW, g.VertSpeed, g.VertSpeedAcc, g.FreqOffset, g.FreqOffsetAcc)
}

// UTC is message 0x32 sub 1, "processUTC_Constellation" (0x3f24-0x42a2, body
// >= 0x14 bytes): UTC date and time, the three DOPs and the number of visible
// satellites. b[9], b[0x10] and b[0x12] are not read by the HAL.
type UTC struct {
	Year                       uint16
	Month, Day, Hour, Min, Sec byte
	PDOP, HDOP, VDOP           float64 // raw/256
	TimeSource                 byte    // b[0x11] bits 4-5
	Visible                    byte    // b[0x13]
}

func (u *UTC) Describe() string {
	return fmt.Sprintf("utc %04d-%02d-%02d %02d:%02d:%02d  pdop %.2f hdop %.2f vdop %.2f  visible %d time=%s",
		u.Year, u.Month, u.Day, u.Hour, u.Min, u.Sec, u.PDOP, u.HDOP, u.VDOP, u.Visible, TimeSource(u.TimeSource))
}

// Channel is message 0x33 sub 1, "processGPSChannel" (0x42a6-0x4656, body >= 9
// bytes): one tracking channel. Flags b[4]: bit0 SV visible, bit1 has been
// tracked, bit4 currently tracking, bit5 SNR meets mask (0x4576-0x4650).
// Extra is b[8], of which the HAL keeps the low two bits and bits 2-3
// separately without logging a name.
type Channel struct {
	Index     byte // b[2] & 0x1f
	PRN       byte
	Flags     byte
	SNR       float64 // raw*0.2
	Azimuth   float64 // 2*raw, degrees
	Elevation float64 // raw, degrees
	Extra     byte
}

func (c *Channel) Describe() string {
	return fmt.Sprintf("channel %d  prn %d snr %.1f az %.0f el %.0f  visible=%v tracked=%v tracking=%v snr-ok=%v extra %02x",
		c.Index, c.PRN, c.SNR, c.Azimuth, c.Elevation, bit(c.Flags, 0), bit(c.Flags, 1), bit(c.Flags, 4), bit(c.Flags, 5), c.Extra)
}

// DPP is message 0x36, "processDPPCalibration" (0x465a-0x46c8). The HAL stores
// a constant 0.0 as the odometer scale and ignores the body, so it is not
// decoded.
type DPP struct{ Body []byte }

func (d *DPP) Describe() string { return fmt.Sprintf("dpp calibration  % x", d.Body) }

// Ext70 is a message 0x70 sub-type the HAL handles but whose body is not
// decoded here. Name is the HAL's function for it (dispatch at 0x46c8).
type Ext70 struct {
	Sub  byte
	Name string
	Body []byte
}

func (e *Ext70) Describe() string {
	return fmt.Sprintf("ext 0x70/0x%02x %s  % x", e.Sub, e.Name, e.Body)
}

// ExtGPSFix is message 0x70 sub 0x22, "processGpsFixData" (0x4ff0-0x50d0, body
// >= 0x58 bytes). Its altitude above the ellipsoid and above mean sea level
// are what the HAL prints in $GPGGA.
type ExtGPSFix struct {
	Status byte    // b[3]
	AltHAE float64 // b[30:38], little-endian float64
	AltMSL float64 // b[38:46]
	Body   []byte
}

func (e *ExtGPSFix) Describe() string {
	return fmt.Sprintf("ext gps fix  status %d alt_hae %.2f alt_msl %.2f", e.Status, e.AltHAE, e.AltMSL)
}

// ExtUsedSV is message 0x70 sub 0x24, "processUsedSVInfo" (0x50d2-0x51ae):
// b[3] is the number of satellites used in the fix and b[4:4+n] their ids
// (1-32 GPS, 65-96 in a second mask).
type ExtUsedSV struct {
	Count byte
	IDs   []byte
}

func (e *ExtUsedSV) Describe() string {
	return fmt.Sprintf("ext used sv  count %d ids %v", e.Count, e.IDs)
}

var ext70Names = map[byte]string{
	0x13: "processDRSensorData (gyro)",
	0x14: "processAccelerometerSensorData",
	0x22: "processGpsFixData",
	0x24: "processUsedSVInfo",
	0x30: "processDRKFOutputData",
	0x31: "processGpsKFOutputData",
	0x33: "processAltitudeFilterOutputData",
	0x50: "processGyroParamDefinition (inferred from the call order)",
	0x51: "processAccelParamDefinition (inferred from the call order)",
}

// Parse decodes a frame body (the module's message, starting with its id).
// A body shorter than the HAL's own length check for the message, or of an id
// or sub-type it does not handle, comes back as *Raw.
func Parse(b []byte) Message {
	raw := func(note string) Message { return &Raw{Body: b, Note: note} }
	if len(b) < 2 {
		return raw("too short")
	}
	short := func(n int) bool { return len(b) < n }
	id, sub := b[0], b[1]
	switch id {
	case IDVersion:
		if sub != 1 {
			return raw("0x11 sub-type not handled by the HAL")
		}
		if short(9) {
			return raw("0x11/1 shorter than the HAL's check (9)")
		}
		return &Version{Major: b[2], Minor: b[3], Build: b[4], Day: b[5], Month: b[6], Year: u16(b, 7)}
	case IDLog:
		switch sub {
		case 1:
			if short(12) {
				return raw("0x14/1 shorter than the HAL's check (12)")
			}
			return &LogEvent{Year: u16(b, 3), Month: b[5], Day: b[6], Hour: b[7], Min: b[8], Sec: b[9], EventCode: b[10], StatusCode: b[11]}
		case 2:
			if short(0x22) {
				return raw("0x14/2 shorter than the HAL's check (34)")
			}
			return &FatalLog{Body: b}
		}
	case IDDRControl:
		switch sub {
		case 1:
			if short(6) {
				return raw("0x21/1 too short for b[4:6]")
			}
			return &DRRate{Rate: be16(b, 4), Body: b}
		case 2:
			if short(8) {
				return raw("0x21/2 too short for b[7]")
			}
			return &Constellation{Version: b[2], Mask: uint16(b[3])<<8 | uint16(b[7]), Body: b}
		}
	case IDProtocol:
		if sub == 2 {
			if short(5) {
				return raw("0x23/2 too short for b[4]")
			}
			return &Protocol{Mask: b[4], Body: b}
		}
	case IDRawDR:
		if sub != 2 {
			return raw("0x30 sub-type not handled by the HAL")
		}
		if short(0x30) {
			return raw("0x30/2 shorter than the HAL's check (48)")
		}
		r := &RawDR{
			Flags2: b[2], Flags3: b[3], AgeIndex: b[4], TOW: u32(b, 5),
			Lat: float64(s32(b, 9)) * degPerLSB, Lon: float64(s32(b, 13)) * degPerLSB,
			Alt: s16(b, 17), Bearing: float64(u16(b, 19)) * bearingPerLSB, SpeedRaw: u16(b, 21),
			DeltaTime: u16(b, 23), DeltaDistance: s16(b, 25), DeltaBearing: s16(b, 27),
			PosAccuracy: u16(b, 29), AltAccuracy: u16(b, 31),
			HeadingAcc: float64(u16(b, 33)) * bearingPerLSB, SpeedAccuracy: u16(b, 35),
			GPSDeltaDistAcc: u16(b, 37), GPSDeltaHeadAcc: u16(b, 39),
			GyroSamples: b[41] & 0x7f, DirSwitch: bit(b[41], 7),
			GyroCount: u32(b, 42), TachoCount: u16(b, 46),
		}
		return r
	case IDGPSFix:
		if sub != 2 {
			return raw("0x31/1 is ignored by the HAL")
		}
		if short(0x28) {
			return raw("0x31/2 shorter than the HAL's check (40)")
		}
		g := &GPSFix{
			TOW: u32(b, 2), Status: b[6] & 0x3f, Alt3D: bit(b[6], 6), DGPS: bit(b[6], 7),
			PosValid: bit(b[7], 0), AltValid: bit(b[7], 1), HeadValid: bit(b[7], 2), SpeedValid: bit(b[7], 3),
			TimeSource: b[7] >> 4 & 3,
			Lat:        float64(s32(b, 8)) * degPerLSB, Lon: float64(s32(b, 12)) * degPerLSB,
			Alt: s16(b, 16), Heading: float64(u16(b, 18)) * bearingPerLSB, Speed: float64(float32(u16(b, 20)) / 100),
			Unk22: s16(b, 22), VertSpeed: u16(b, 0x1e), VertSpeedAcc: u16(b, 0x20),
			FreqOffset: s32(b, 0x22), FreqOffsetAcc: u16(b, 0x26),
		}
		return g
	case IDUTC:
		if sub != 1 {
			return raw("0x32 sub-type not handled by the HAL")
		}
		if short(0x14) {
			return raw("0x32/1 shorter than the HAL's check (20)")
		}
		return &UTC{
			Year: u16(b, 2), Month: b[4], Day: b[5], Hour: b[6], Min: b[7], Sec: b[8],
			PDOP: float64(u16(b, 10)) * dopPerLSB, HDOP: float64(u16(b, 12)) * dopPerLSB, VDOP: float64(u16(b, 14)) * dopPerLSB,
			TimeSource: b[0x11] >> 4 & 3, Visible: b[0x13],
		}
	case IDChannel:
		if sub != 1 {
			return raw("0x33 sub-type not handled by the HAL")
		}
		if short(9) {
			return raw("0x33/1 shorter than the HAL's check (9)")
		}
		return &Channel{
			Index: b[2] & 0x1f, PRN: b[3], Flags: b[4], SNR: float64(float32(b[5])) * snrPerLSB,
			Azimuth: float64(2 * float32(b[6])), Elevation: float64(b[7]), Extra: b[8],
		}
	case IDDPP:
		return &DPP{Body: b}
	case IDExt:
		name, ok := ext70Names[sub]
		if !ok {
			return raw("0x70 sub-type not handled by the HAL")
		}
		switch sub {
		case 0x22:
			if short(0x58) {
				return raw("0x70/0x22 shorter than the HAL's check (88)")
			}
			return &ExtGPSFix{Status: b[3], AltHAE: f64le(b, 30), AltMSL: f64le(b, 38), Body: b}
		case 0x24:
			if short(4) {
				return raw("0x70/0x24 too short")
			}
			n := int(b[3])
			if 4+n > len(b) {
				n = len(b) - 4
			}
			return &ExtUsedSV{Count: b[3], IDs: append([]byte(nil), b[4:4+n]...)}
		}
		return &Ext70{Sub: sub, Name: name, Body: b}
	}
	return raw(fmt.Sprintf("id 0x%02x sub 0x%02x not handled", id, sub))
}

// Hex is body as spaced hex, for dumps.
func Hex(b []byte) string {
	var sb strings.Builder
	for i, c := range b {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%02x", c)
	}
	return sb.String()
}
