package hippo

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTapSpeed(t *testing.T) {
	for _, tc := range []struct {
		line string
		kmh  float64
		ok   bool
	}{
		// Byte 1 is 0.5 mph: 0x86 is 67 mph.
		{"15:04:05.000 rx hdr=44c100c4 cmd=20 payload=0086  # Vehicle speed: Vehicle speed=107.826048 km/h", 107.826048, true},
		{"15:04:05.000 rx hdr=44c100c4 cmd=20 payload=0000", 0, true},
		{"15:04:05.000 rx hdr=44c100c4 cmd=20 payload=00", 0, false},       // too short to decode
		{"15:04:05.000 rx hdr=0d8f018d cmd=47 payload=9b006f54", 0, false}, // the battery
		{"15:04:05.000 tx hdr=44c100c4 cmd=20 payload=0086", 0, false},     // not received
		{"15:04:05.000 rx hdr=44c100c4 cmd=20 payload=zz", 0, false},       // not hex
		{"15:04:05.000 rx hdr=44c1 cmd=20 payload=0086", 0, false},         // short header
		{"garbage", 0, false},
	} {
		kmh, ok := tapSpeed(tc.line)
		if ok != tc.ok || ok && kmh != tc.kmh {
			t.Errorf("%q: %v %v, want %v %v", tc.line, kmh, ok, tc.kmh, tc.ok)
		}
	}
}

// While the car's speed stands and the module has a fix, it is the speed of
// $GPRMC (knots) and $GPVTG (knots and km/h); the other sentences, and every
// sentence when it does not, are the module's. It stands while micomd's tap
// is connected and talking, a steady speed (no new speed frame, as the micom
// sends one only on change) included, up to MaxAge. The course is always the
// DR bearing, or with CourseStatus empty while the heading status is clear.
func TestConverterCarSpeed(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	const s = time.Second
	for _, tc := range []struct {
		name      string
		fix       byte    // the module's fix status, 0 for none
		kmh       float64 // the car's last reading, -1 for none
		speedAge  time.Duration
		lineAge   time.Duration // since the last tap line of any kind
		connected bool
		status    bool   // CourseStatus
		flags2    byte   // the DR flags; 0xfe has the heading status bit
		rmc       string // RMC speed field
		vtgKnots  string
		vtgKmh    string
		course    string // RMC and VTG
		logged    string
	}{
		{"car speed", FixValid, 100, s, s, true, false, 0xfe, "54.0", "54.0", "100.0", "90.0", "the car's"},
		{"car stopped", FixValid, 0, 0, 0, true, false, 0xfe, "0.0", "0.0", "0.0", "90.0", "the car's"},
		{"67 mph", FixValid, 107.826048, 0, 0, true, false, 0xfe, "58.2", "58.2", "107.8", "90.0", "the car's"},
		{"steady: no speed frame for 20 s, other frames arriving", FixValid, 100, 20 * s, s / 2, true, false, 0xfe, "54.0", "54.0", "100.0", "90.0", "the car's"},
		{"no speed frame for 40 s", FixValid, 100, 40 * s, s / 2, true, false, 0xfe, "19.4", "19.4", "36.0", "90.0", ""},
		{"tap quiet: micomd's link down", FixValid, 100, 5 * s, 5 * s, true, false, 0xfe, "19.4", "19.4", "36.0", "90.0", ""},
		{"tap disconnected", FixValid, 100, s, s, false, false, 0xfe, "19.4", "19.4", "36.0", "90.0", ""},
		{"absent", FixValid, -1, 0, 0, true, false, 0xfe, "19.4", "19.4", "36.0", "90.0", ""},
		{"no fix", 0, 100, 0, 0, true, false, 0xfe, "19.4", "19.4", "36.0", "90.0", ""},
		{"heading status ignored", FixValid, -1, 0, 0, true, false, 0xfa, "19.4", "19.4", "36.0", "90.0", ""},
		{"heading status set", FixValid, 100, 0, 0, true, true, 0xfe, "54.0", "54.0", "100.0", "90.0", "the car's"},
		{"heading status clear", FixValid, 100, 0, 0, true, true, 0xfa, "54.0", "54.0", "100.0", "", "the car's"},
	} {
		var logs []string
		cs := &CarSpeed{MaxAge: 30 * s, LinkAge: 3 * s, now: func() time.Time { return now },
			Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}
		cs.connected, cs.lineAt = tc.connected, now.Add(-tc.lineAge)
		if tc.kmh >= 0 {
			cs.kmh, cs.at = tc.kmh, now.Add(-tc.speedAge)
		}
		c := Converter{Speed: cs, CourseStatus: tc.status}
		raw := packRawDR(48.1173, 11.516666666, 0x4000, 1000, 0)
		raw[2] = tc.flags2
		out := feed(&c, utcBody(9), fixBody(tc.fix), raw)
		for _, s := range out {
			checkSentence(t, s)
		}
		rmc, vtg := pick(out, "GPRMC"), pick(out, "GPVTG")
		if rmc == nil || vtg == nil {
			t.Fatalf("%s: %q", tc.name, out)
		}
		if rmc[7] != tc.rmc || rmc[8] != tc.course || vtg[5] != tc.vtgKnots || vtg[7] != tc.vtgKmh || vtg[1] != tc.course {
			t.Errorf("%s: RMC %v VTG %v", tc.name, rmc, vtg)
		}
		if got := strings.Join(logs, "|"); !strings.Contains(got, tc.logged) || tc.logged == "" && got != "" {
			t.Errorf("%s: logged %q", tc.name, got)
		}
	}
}

// The source change is logged once each way, not per sentence.
func TestCarSpeedLogsTheSwitch(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	var logs []string
	cs := &CarSpeed{MaxAge: 30 * time.Second, LinkAge: 3 * time.Second, now: func() time.Time { return now },
		Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}
	cs.Set(50)
	for range 3 {
		cs.Knots()
	}
	now = now.Add(5 * time.Second)
	for range 3 {
		cs.Knots()
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "car's") || !strings.Contains(logs[1], "module's") {
		t.Errorf("logged %q", logs)
	}
}

// The tap is followed into CarSpeed, across micomd restarts; the reading
// stands while the tap talks and drops when it disconnects.
func TestFollowSpeedTap(t *testing.T) {
	dir, err := os.MkdirTemp("", "tap")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "micomd.sock")
	ln, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cs := &CarSpeed{MaxAge: time.Minute, LinkAge: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go FollowSpeedTap(ctx, p, cs, t.Logf)
	state := func() string {
		k, ok := cs.Knots()
		return fmt.Sprintf("%.1f %v", k*1.852, ok)
	}
	waitFor := func(want string) {
		t.Helper()
		for i := 0; i < 300 && state() != want; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		if got := state(); got != want {
			t.Errorf("%s, want %s", got, want)
		}
	}
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(c, "15:04:05.000 rx hdr=44c100c4 cmd=20 payload=0086")
	waitFor("107.8 true")
	c.Close()
	waitFor("107.8 false")
	if c, err = ln.Accept(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintln(c, "15:04:05.000 rx hdr=018a0181 cmd=0f payload=00")
	waitFor("107.8 false") // talking, but the reading went with the old connection
	fmt.Fprintln(c, "15:04:05.000 rx hdr=44c100c4 cmd=20 payload=0050")
	waitFor("64.4 true") // 0x50 = 40 mph
}
