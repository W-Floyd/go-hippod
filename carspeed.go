package hippo

import (
	"bufio"
	"context"
	"encoding/hex"
	"net"
	"strings"
	"sync"
	"time"

	micom "github.com/W-Floyd/go-micomd"
)

// CarSpeed is the car's own speed, as micomd decodes it from the micom, for
// the NMEA in place of the module's: Android Auto and Maps then show the
// speedometer's speed. It is used only while fresh; a stale or absent one
// leaves the module's.
type CarSpeed struct {
	// MaxAge is how long a reading stays fresh.
	MaxAge time.Duration
	// Logf hears when the car's speed takes over and when it falls back.
	Logf func(format string, args ...any)
	now  func() time.Time

	mu    sync.Mutex
	kmh   float64
	at    time.Time
	using bool
}

// Set records a reading in km/h, taken now.
func (c *CarSpeed) Set(kmh float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kmh, c.at = kmh, c.clock()
}

// Knots is the car's speed in knots while fresh. It logs a change of source.
func (c *CarSpeed) Knots() (float64, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	fresh := !c.at.IsZero() && c.clock().Sub(c.at) <= c.MaxAge
	if fresh != c.using && c.Logf != nil {
		if fresh {
			c.Logf("speed: the car's, from micomd")
		} else {
			c.Logf("speed: the GNSS module's (no fresh car speed for %v)", c.MaxAge)
		}
	}
	c.using = fresh
	return c.kmh / 1.852, fresh
}

func (c *CarSpeed) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// carSpeedField is the variant's decoded speed (variants/kia-ev6-gen5w.yaml).
const carSpeedField = "Vehicle speed"

// FollowSpeedTap reads micomd's tap at path until ctx ends, setting c from
// each speed frame, and reconnects (1 s, doubling to 30 s) when it cannot.
// It never touches the NMEA path: Knots reads what it last set.
func FollowSpeedTap(ctx context.Context, path string, c *CarSpeed, logf func(string, ...any)) {
	wait, said := time.Second, ""
	for ctx.Err() == nil {
		err := followOnce(ctx, path, c)
		if err != nil && err.Error() != said {
			logf("speed tap %s: %v", path, err)
			said = err.Error()
		}
		if err == nil {
			wait, said = time.Second, ""
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, 30*time.Second)
	}
}

func followOnce(ctx context.Context, path string, c *CarSpeed) error {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		if kmh, ok := tapSpeed(sc.Text()); ok {
			c.Set(kmh)
		}
	}
	return sc.Err()
}

// tapSpeed is the car's speed in km/h from one line of micomd's tap, when
// the line is a speed frame.
func tapSpeed(line string) (float64, bool) {
	f, ok := parseTapLine(line)
	if !ok {
		return 0, false
	}
	for _, fl := range micom.Decode(f) {
		if fl.Name != carSpeedField || !fl.Valid {
			continue
		}
		if fl.Float != nil {
			return *fl.Float, true
		}
		return float64(fl.Value), true
	}
	return 0, false
}

// parseTapLine reads a received frame off one line of micomd's tap:
// "15:04:05.000 rx hdr=44c100c4 cmd=20 payload=0086  # Vehicle speed: ...".
// The text after # is for people and is not read.
func parseTapLine(line string) (micom.Frame, bool) {
	var f micom.Frame
	line, _, _ = strings.Cut(line, "#")
	fields := strings.Fields(line)
	if len(fields) != 5 || fields[1] != "rx" {
		return f, false
	}
	hdr, ok1 := tapField(fields[2], "hdr=")
	cmd, ok2 := tapField(fields[3], "cmd=")
	payload, ok3 := tapField(fields[4], "payload=")
	if !ok1 || !ok2 || !ok3 || len(hdr) != 4 || len(cmd) != 1 {
		return f, false
	}
	copy(f.Hdr[:], hdr)
	f.Cmd = cmd[0]
	f.Payload = payload
	return f, true
}

func tapField(s, prefix string) ([]byte, bool) {
	v, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return nil, false
	}
	b, err := hex.DecodeString(v)
	return b, err == nil
}
