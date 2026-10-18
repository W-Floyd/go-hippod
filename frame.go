// Package hippo speaks the "hippo" serial protocol of the GNSS module in the
// Kia EV6 head unit (Mobis DAudio Plus Gen5 Wide, Telechips TCC8031), and turns
// its fixes into NMEA so that gpsd can use them. The stock Android HAL
// (gps.tcc803x.so) is the only source: it parses hippo frames and builds its
// own $GPGGA/$GPRMC/$GPGSV, and this package follows what it does.
//
// Everything here was read from a disassembly of that library, not a
// specification, and no capture from a unit exists yet. Each decoded layout
// cites the address of the HAL code it comes from; a field with no citation is
// a guess and is exposed raw. See the README for the full list.
//
// # Link
//
// 115200 baud, 8N1, raw (the HAL's open_serial, 0x5f18), on the PL011 the
// vendor calls /dev/ttyTCC3.
//
// # Frame
//
//	81 | body... | csum | 82
//
// csum = -(0x81 + sum(body) + 0x82) & 0xff: the HAL's calc_csum (0x3790) is
// seeded with 3, the low byte of 0x81+0x82, and negates the byte sum. Inside a
// frame 0x80 is an escape: 0x80 X (X <= 7) stands for X|0x80, so 0x80, 0x81
// and 0x82 travel as 80 00, 80 01, 80 02 (reader hippo_proc, 0x8bd4-0x8d02).
// The HAL's own writers never escape, which is harmless for every frame they
// build. The body starts with a class byte when the host sends (01 set,
// 02 query, 03 reset) and with the message id when the module does.
package hippo

import "fmt"

const (
	// SOF and EOF delimit a frame.
	SOF = 0x81
	EOF = 0x82
	// Esc starts a two-byte escape for 0x80..0x82 inside a frame.
	Esc = 0x80
	// MaxBody bounds a frame; the HAL's buffer is 0x100 bytes (0x8bc4).
	MaxBody = 0x100
)

// Sum is the frame checksum of body.
func Sum(body []byte) byte {
	s := byte(SOF)
	s += EOF
	for _, b := range body {
		s += b
	}
	return -s
}

// Encode is body as a frame on the wire, checksum and escapes included.
func Encode(body []byte) []byte {
	out := make([]byte, 0, len(body)+4)
	out = append(out, SOF)
	put := func(b byte) {
		if b >= Esc && b <= EOF {
			out = append(out, Esc, b&0x7f)
			return
		}
		out = append(out, b)
	}
	for _, b := range body {
		put(b)
	}
	put(Sum(body))
	return append(out, EOF)
}

// Frame is one received frame.
type Frame struct {
	// Body is the unescaped content before the checksum.
	Body []byte
	// Sum is the received checksum byte.
	Sum byte
	// Valid reports whether Sum matches Body.
	Valid bool
}

func (f Frame) String() string {
	ok := "ok"
	if !f.Valid {
		ok = fmt.Sprintf("BAD csum, want %02x", Sum(f.Body))
	}
	return fmt.Sprintf("% x  csum %02x %s", f.Body, f.Sum, ok)
}

// Decoder turns a byte stream into frames. Like the HAL's reader it skips
// anything outside a frame, drops a frame that is restarted by another 0x81
// and one with a bad escape or too long, so it resynchronises on garbage.
type Decoder struct {
	state   int
	buf     []byte
	garbage int
	dropped int
}

const (
	stIdle = iota
	stBody
	stEsc
)

// Garbage is how many bytes were skipped outside frames.
func (d *Decoder) Garbage() int { return d.garbage }

// Dropped is how many partial frames were abandoned.
func (d *Decoder) Dropped() int { return d.dropped }

func (d *Decoder) reset() { d.state = stIdle; d.buf = d.buf[:0] }

func (d *Decoder) abort() {
	d.dropped++
	d.reset()
}

// Write feeds bytes and returns the frames they completed. It never fails.
func (d *Decoder) Write(p []byte) []Frame {
	var out []Frame
	for _, b := range p {
		switch d.state {
		case stIdle:
			if b == SOF {
				d.state = stBody
				d.buf = d.buf[:0]
			} else {
				d.garbage++
			}
		case stBody:
			switch {
			case b == SOF:
				d.dropped++
				d.buf = d.buf[:0]
			case b == EOF:
				if len(d.buf) >= 2 {
					n := len(d.buf) - 1
					f := Frame{Body: append([]byte(nil), d.buf[:n]...), Sum: d.buf[n]}
					f.Valid = Sum(f.Body) == f.Sum
					out = append(out, f)
				} else {
					d.dropped++
				}
				d.reset()
			case b == Esc:
				d.state = stEsc
			default:
				d.add(b)
			}
		case stEsc:
			if b > 7 {
				d.abort()
				continue
			}
			d.state = stBody
			d.add(b | 0x80)
		}
	}
	return out
}

func (d *Decoder) add(b byte) {
	if len(d.buf) >= MaxBody+1 {
		d.abort()
		return
	}
	d.buf = append(d.buf, b)
}
