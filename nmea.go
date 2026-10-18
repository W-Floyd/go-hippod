package hippo

import (
	"fmt"
	"math"
	"strings"
)

// NMEAChecksum is the XOR of the characters between '$' and '*'.
func NMEAChecksum(body string) byte {
	var x byte
	for i := 0; i < len(body); i++ {
		x ^= body[i]
	}
	return x
}

// Sentence frames body (without '$' and '*') as "$body*HH\r\n".
func Sentence(body string) string {
	return fmt.Sprintf("$%s*%02X\r\n", body, NMEAChecksum(body))
}

// coord formats a signed degree value as NMEA "ddmm.mmmmmm" (width 4 for
// latitude, 5 for longitude) and its hemisphere letter. The HAL's
// deg_to_nmea2 (0x6ec8) does the same arithmetic in floating point and
// truncates; this rounds to a millionth of a minute and carries, so 59.9999996
// does not print as 59.999999.
func coord(deg float64, width int, pos, neg byte) (string, byte) {
	h := pos
	if deg < 0 {
		h = neg
		deg = -deg
	}
	micro := int64(math.Round(deg * 60e6))
	d, m := micro/60e6, micro%int64(60e6)
	return fmt.Sprintf("%0*d.%06d", width, d*100+m/1e6, m%1e6), h
}

// GGAData is what $GPGGA needs.
type GGAData struct {
	Hour, Min, Sec int
	HasPos         bool
	Lat, Lon       float64
	Quality        int
	Sats           int
	HDOP           float64
	HasAlt         bool
	AltMSL, Sep    float64
	HasSep         bool // the geoid separation is known; empty when not
}

// GGA is the $GPGGA sentence, laid out like the HAL's template at 0xb810:
// "$GPGGA,%02d%02d%02d.00,lat,N,lon,E,%d,%02d,%.1f,%.1f,M,%.1f,M,,".
func GGA(g GGAData) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "GPGGA,%02d%02d%02d.00,", g.Hour, g.Min, g.Sec)
	if g.HasPos {
		la, lh := coord(g.Lat, 4, 'N', 'S')
		lo, oh := coord(g.Lon, 5, 'E', 'W')
		fmt.Fprintf(&sb, "%s,%c,%s,%c,", la, lh, lo, oh)
	} else {
		sb.WriteString(",,,,")
	}
	fmt.Fprintf(&sb, "%d,%02d,%.1f,", g.Quality, g.Sats, g.HDOP)
	if g.HasAlt {
		fmt.Fprintf(&sb, "%.1f,M,", g.AltMSL)
		if g.HasSep {
			fmt.Fprintf(&sb, "%.1f", g.Sep)
		}
		sb.WriteString(",M,,")
	} else {
		sb.WriteString(",M,,M,,")
	}
	return Sentence(sb.String())
}

// RMCData is what $GPRMC needs.
type RMCData struct {
	Hour, Min, Sec   int
	Day, Month, Year int // Year is the full year
	Valid            bool
	HasPos           bool
	Lat, Lon         float64
	Knots, Course    float64
	Mode             byte
}

// RMC is the $GPRMC sentence, laid out like the HAL's template at 0xb8b2. The
// HAL's magnetic variation comes from a sensor value outside the traced
// messages, so those two fields are left empty.
func RMC(r RMCData) string {
	st := byte('V')
	if r.Valid {
		st = 'A'
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "GPRMC,%02d%02d%02d.00,%c,", r.Hour, r.Min, r.Sec, st)
	if r.HasPos {
		la, lh := coord(r.Lat, 4, 'N', 'S')
		lo, oh := coord(r.Lon, 5, 'E', 'W')
		fmt.Fprintf(&sb, "%s,%c,%s,%c,", la, lh, lo, oh)
	} else {
		sb.WriteString(",,,,")
	}
	fmt.Fprintf(&sb, "%.1f,%.1f,%02d%02d%02d,,,%c", r.Knots, r.Course, r.Day, r.Month, r.Year%100, r.Mode)
	return Sentence(sb.String())
}

// VTG is the $GPVTG sentence (not in the HAL; the same speed and course).
func VTG(course, knots float64, mode byte) string {
	return Sentence(fmt.Sprintf("GPVTG,%.1f,T,,M,%.1f,N,%.1f,K,%c", course, knots, knots*1.852, mode))
}

// SatView is one satellite of a GSV sentence.
type SatView struct {
	PRN, Elev, Az, SNR int
	Known              bool
}

// emptyGSV is the HAL's fixed sentence for no visible satellites (0xb927).
const emptyGSV = "GPGSV,,,,,,,,,,,,,,,,,,,"

// GSV returns the $GPGSV sentences for sats: four per sentence, at most six, as
// the HAL does (0x81cc-0x8308). The sat count in each header is visible, which
// may exceed len(sats) when the HAL's six-sentence cap applies.
func GSV(sats []SatView, visible int) []string {
	if visible <= 0 {
		return []string{Sentence(emptyGSV)}
	}
	total := (visible + 3) / 4
	if total > 6 {
		total = 6
	}
	var out []string
	for k := 1; k <= total; k++ {
		var sb strings.Builder
		fmt.Fprintf(&sb, "GPGSV,%d,%d,%d", total, k, visible)
		for i := 4 * (k - 1); i < 4*k && i < visible; i++ {
			if i < len(sats) && sats[i].Known {
				s := sats[i]
				fmt.Fprintf(&sb, ",%d,%d,%d,%d", s.PRN, s.Elev, s.Az, s.SNR)
			} else {
				sb.WriteString(",,,,")
			}
		}
		out = append(out, Sentence(sb.String()))
	}
	return out
}
