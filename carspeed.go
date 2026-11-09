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
// speedometer's speed. The micom sends the speed frame only on change, so a
// steady speed goes unrepeated: the last reading stands while the tap is
// connected and micomd's link is alive (any tap line within LinkAge), up to
// MaxAge after it arrived. Otherwise the module's speed stays.
type CarSpeed struct {
	// LinkAge is how long the tap may be silent; the micom's routine frames
	// (its CPU-alive requests, the answers to micomd's 1 s poll) keep it
	// talking while the link is up.
	LinkAge time.Duration
	// MaxAge is how long a reading stands without a new one.
	MaxAge time.Duration
	// Logf hears when the car's speed takes over and when it falls back.
	Logf func(format string, args ...any)
	now  func() time.Time

	mu        sync.Mutex
	kmh       float64
	at        time.Time // the last reading
	lineAt    time.Time // the last tap line of any kind
	connected bool
	using     bool
}

// Set records a reading in km/h, taken now, off a connected tap.
func (c *CarSpeed) Set(kmh float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kmh, c.at, c.lineAt, c.connected = kmh, c.clock(), c.clock(), true
}

// Line records a tap line that carries no speed: the link is alive.
func (c *CarSpeed) Line() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lineAt, c.connected = c.clock(), true
}

// Disconnected drops the reading: the tap is gone.
func (c *CarSpeed) Disconnected() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected, c.at = false, time.Time{}
}

// Knots is the car's speed in knots while it stands. It logs a change of
// source.
func (c *CarSpeed) Knots() (float64, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	fresh := c.connected && !c.at.IsZero() && now.Sub(c.lineAt) <= c.LinkAge && now.Sub(c.at) <= c.MaxAge
	if fresh != c.using && c.Logf != nil {
		if fresh {
			c.Logf("speed: the car's, from micomd")
		} else {
			c.Logf("speed: the GNSS module's (micomd's tap down or quiet, or no car speed for %v)", c.MaxAge)
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
	defer c.Disconnected()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		if kmh, ok := tapSpeed(sc.Text()); ok {
			c.Set(kmh)
		} else {
			c.Line()
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
