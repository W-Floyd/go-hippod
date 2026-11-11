// Command hippo-fake plays the GNSS module's side of its UART for emulators:
// the stock head unit under QEMU (ttyTCC3, which the kernel's dupttyd turns
// into the HAL's ttyGPS0) or hippod in a VM. It answers the HAL's version and
// protocol queries and sends a fix and UTC every second, from a GPX track or
// a still point. Its fixes are a fake: never point it at a real module.
package main

import (
	"context"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	hippo "github.com/W-Floyd/go-hippod"
)

type point struct {
	t        time.Time
	lat, lon float64
}

// track is where the fake is at time t since the start: interpolated along
// the GPX points by their times (1 s apart where a point has none), its
// speed and heading from the segment.
type track struct{ pts []point }

func (k track) at(d time.Duration) (p point, speed, heading float64) {
	if len(k.pts) == 1 {
		p = k.pts[0]
		p.t = p.t.Add(d) // a still point's clock runs on: the HAL refuses a repeated time
		return p, 0, 0
	}
	t := k.pts[0].t.Add(d)
	for i := 1; i < len(k.pts); i++ {
		a, b := k.pts[i-1], k.pts[i]
		if t.After(b.t) && i < len(k.pts)-1 {
			continue
		}
		span := b.t.Sub(a.t).Seconds()
		f := math.Min(math.Max(t.Sub(a.t).Seconds()/span, 0), 1)
		p = point{t, a.lat + f*(b.lat-a.lat), a.lon + f*(b.lon-a.lon)}
		dist, brg := distance(a, b)
		if f < 1 {
			speed = dist / span
		}
		return p, speed, brg
	}
	return k.pts[len(k.pts)-1], 0, 0
}

func (k track) length() time.Duration { return k.pts[len(k.pts)-1].t.Sub(k.pts[0].t) }

// distance is metres and initial bearing in degrees from a to b.
func distance(a, b point) (float64, float64) {
	const r = 6371000.0
	la1, la2 := a.lat*math.Pi/180, b.lat*math.Pi/180
	dla, dlo := la2-la1, (b.lon-a.lon)*math.Pi/180
	h := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(la1)*math.Cos(la2)*math.Sin(dlo/2)*math.Sin(dlo/2)
	brg := math.Atan2(math.Sin(dlo)*math.Cos(la2), math.Cos(la1)*math.Sin(la2)-math.Sin(la1)*math.Cos(la2)*math.Cos(dlo))
	return 2 * r * math.Asin(math.Sqrt(h)), math.Mod(brg*180/math.Pi+360, 360)
}

func loadGPX(path string) (track, error) {
	f, err := os.Open(path)
	if err != nil {
		return track{}, err
	}
	defer f.Close()
	var g struct {
		Pts []struct {
			Lat  float64 `xml:"lat,attr"`
			Lon  float64 `xml:"lon,attr"`
			Time string  `xml:"time"`
		} `xml:"trk>trkseg>trkpt"`
	}
	if err := xml.NewDecoder(f).Decode(&g); err != nil {
		return track{}, err
	}
	if len(g.Pts) == 0 {
		return track{}, fmt.Errorf("%s: no trkpt", path)
	}
	var k track
	start := time.Now().UTC().Truncate(time.Second)
	for i, p := range g.Pts {
		t, err := time.Parse(time.RFC3339, p.Time)
		if err != nil {
			t = start.Add(time.Duration(i) * time.Second)
		}
		k.pts = append(k.pts, point{t, p.Lat, p.Lon})
	}
	return k, nil
}

type fake struct {
	mu   sync.Mutex
	w    io.Writer
	logf func(string, ...any)
}

func (f *fake) send(body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.w != nil {
		f.w.Write(hippo.Encode(body))
	}
}

// answer replies to the HAL's queries as the module does; everything else
// is logged.
func (f *fake) answer(fr hippo.Frame) {
	b := fr.Body
	for _, s := range hippo.Stock {
		if string(s.Body) == string(b) {
			f.logf("host %s", s.Name)
			switch s.Name {
			case "query-version":
				f.send((&hippo.Version{Major: 6, Minor: 12, Build: 7, Year: 2023, Month: 6, Day: 21}).Body())
			case "query-protocol":
				f.send(hippo.ProtocolBody(3))
			}
			return
		}
	}
	f.logf("host % x", b)
}

// fixes sends a UTC and a fix every second, the time the route's.
func (f *fake) fixes(ctx context.Context, k track, loop bool) {
	start := time.Now()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		d := time.Since(start)
		if loop && k.length() > 0 {
			d %= k.length()
		}
		p, speed, heading := k.at(d)
		u := p.t.UTC()
		f.send((&hippo.UTC{Year: uint16(u.Year()), Month: byte(u.Month()), Day: byte(u.Day()), Hour: byte(u.Hour()),
			Min: byte(u.Minute()), Sec: byte(u.Second()), PDOP: 1.6, HDOP: 0.9, VDOP: 1.3, TimeSource: 3, Visible: 10}).Body())
		wd := int(u.Weekday())
		tow := uint32(wd*86400 + u.Hour()*3600 + u.Minute()*60 + u.Second())
		// The HAL's position is the DR solution's: speed, heading and
		// altitude valid (Flags2 b1-3), a DR solution with GPS time (Flags3
		// b7, b4-5).
		f.send((&hippo.RawDR{Flags2: 0x0e, Flags3: 0x80 | 3<<4, TOW: tow, Lat: p.lat, Lon: p.lon, Alt: 30, Bearing: heading,
			SpeedRaw: uint16(math.Round(speed * 100)), DeltaTime: 1000, PosAccuracy: 5, AltAccuracy: 10, HeadingAcc: 2,
			SpeedAccuracy: 10}).Body())
		f.send((&hippo.GPSFix{TOW: tow, Status: hippo.FixValid, Alt3D: true, PosValid: true, AltValid: true,
			HeadValid: speed > 0.5, SpeedValid: true, TimeSource: 3, Lat: p.lat, Lon: p.lon, Alt: 30,
			Heading: heading, Speed: speed}).Body())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func main() {
	connect := flag.String("connect", "", "the host end: unix:PATH or tcp:HOST:PORT (a QEMU chardev server)")
	gpx := flag.String("gpx", "", "GPX track to drive (its points' times pace it)")
	at := flag.String("point", "", "or a still point: LAT,LON")
	loop := flag.Bool("loop", false, "drive the track again and again")
	flag.Parse()
	logger := log.New(os.Stderr, "hippo-fake: ", log.Lmicroseconds)
	var k track
	switch {
	case *gpx != "":
		var err error
		if k, err = loadGPX(*gpx); err != nil {
			logger.Fatal(err)
		}
	case *at != "":
		la, lo, _ := strings.Cut(*at, ",")
		lat, err1 := strconv.ParseFloat(la, 64)
		lon, err2 := strconv.ParseFloat(lo, 64)
		if err1 != nil || err2 != nil {
			logger.Fatal("-point LAT,LON")
		}
		k = track{[]point{{time.Now().UTC(), lat, lon}}}
	default:
		logger.Fatal("give -gpx or -point")
	}
	kind, addr, _ := strings.Cut(*connect, ":")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	f := &fake{logf: logger.Printf}
	for ctx.Err() == nil {
		c, err := net.Dial(kind, addr)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		logger.Printf("connected")
		f.mu.Lock()
		f.w = c
		f.mu.Unlock()
		cctx, cancel := context.WithCancel(ctx)
		go f.fixes(cctx, k, *loop)
		var d hippo.Decoder
		buf := make([]byte, 1024)
		for {
			n, err := c.Read(buf)
			for _, fr := range d.Write(buf[:n]) {
				f.answer(fr)
			}
			if err != nil {
				break
			}
		}
		cancel()
		c.Close()
	}
}
