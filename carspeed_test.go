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

// While fresh and with a fix, the car's speed is the speed of $GPRMC (knots)
// and $GPVTG (knots and km/h); the other sentences, and every sentence when it
// is stale, absent or without a fix, are the module's.
func TestConverterCarSpeed(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		fix      byte    // the module's fix status, 0 for none
		kmh      float64 // the car's, -1 for none
		age      time.Duration
		rmc      string // RMC speed field
		vtgKnots string
		vtgKmh   string
		logged   string
	}{
		{"car speed", FixValid, 100, time.Second, "54.0", "54.0", "100.0", "the car's"},
		{"car stopped", FixValid, 0, 0, "0.0", "0.0", "0.0", "the car's"},
		{"67 mph", FixValid, 107.826048, 0, "58.2", "58.2", "107.8", "the car's"},
		{"stale", FixValid, 100, 3 * time.Second, "19.4", "19.4", "36.0", ""},
		{"absent", FixValid, -1, 0, "19.4", "19.4", "36.0", ""},
		{"no fix", 0, 100, 0, "19.4", "19.4", "36.0", ""},
	} {
		var logs []string
		cs := &CarSpeed{MaxAge: 2 * time.Second, Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}
		cs.now = func() time.Time { return now.Add(-tc.age) }
		if tc.kmh >= 0 {
			cs.Set(tc.kmh)
		}
		cs.now = func() time.Time { return now }
		c := Converter{Speed: cs}
		out := feed(&c, utcBody(9), fixBody(tc.fix), packRawDR(48.1173, 11.516666666, 0x4000, 1000, 0))
		for _, s := range out {
			checkSentence(t, s)
		}
		rmc, vtg := pick(out, "GPRMC"), pick(out, "GPVTG")
		if rmc == nil || vtg == nil {
			t.Fatalf("%s: %q", tc.name, out)
		}
		if rmc[7] != tc.rmc || rmc[8] != "90.0" || vtg[5] != tc.vtgKnots || vtg[7] != tc.vtgKmh || vtg[1] != "90.0" {
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
	cs := &CarSpeed{MaxAge: 2 * time.Second, now: func() time.Time { return now },
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

// The tap is followed into CarSpeed, and across micomd restarts.
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
	cs := &CarSpeed{MaxAge: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go FollowSpeedTap(ctx, p, cs, t.Logf)
	for _, payload := range []string{"0086", "0050"} {
		c, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(c, "15:04:05.000 rx hdr=44c100c4 cmd=20 payload=%s\n", payload)
		c.Close()
	}
	kmh := func() string { k, _ := cs.Knots(); return fmt.Sprintf("%.1f", k*1.852) }
	for i := 0; i < 200 && kmh() != "64.4"; i++ { // 0x50 = 40 mph
		time.Sleep(10 * time.Millisecond)
	}
	if got := kmh(); got != "64.4" {
		t.Errorf("%s km/h", got)
	}
}
