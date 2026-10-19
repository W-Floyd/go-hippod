package hippo

import (
	"context"
	"fmt"
	"io"
	"time"
)

// RunConfig is what Run does with a port.
type RunConfig struct {
	// Out receives the NMEA sentences; nil discards them.
	Out io.Writer
	// Dump receives one line per frame with its decoded fields; nil for none.
	Dump io.Writer
	// Send is written to the port, frame by frame and spaced by Gap, before
	// reading starts. Only StockFrames can be given.
	Send []StockFrame
	// Gap is the pause between sent frames; zero means 100 ms.
	Gap time.Duration
	// Wait ends Run after this long when positive (used by -query).
	Wait time.Duration
	// AltFromDR is Converter.AltFromDR.
	AltFromDR bool
	// Extra is Converter.Extra.
	Extra bool
	// Logf reports sent frames and the final counters; nil for none.
	Logf func(format string, args ...any)
}

// Stats counts what Run saw.
type Stats struct {
	Frames, BadSum, Garbage, Dropped, Sentences int
}

func (s Stats) String() string {
	return fmt.Sprintf("frames %d, bad checksums %d, garbage bytes %d, dropped partial frames %d, NMEA sentences %d",
		s.Frames, s.BadSum, s.Garbage, s.Dropped, s.Sentences)
}

// Run reads hippo frames from port until ctx ends, the port fails or Wait
// passes, dumps and converts them. It closes port (when it is an io.Closer)
// on return, which also ends a blocked Read.
func Run(ctx context.Context, port io.ReadWriter, cfg RunConfig) (Stats, error) {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if c, ok := port.(io.Closer); ok {
		defer c.Close()
	}
	var st Stats
	var dec Decoder
	conv := &Converter{AltFromDR: cfg.AltFromDR, Extra: cfg.Extra}

	type chunk struct {
		b   []byte
		err error
	}
	reads := make(chan chunk)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := port.Read(buf)
			var b []byte
			if n > 0 {
				b = append(b, buf[:n]...)
			}
			select {
			case reads <- chunk{b, err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	gap := cfg.Gap
	if gap == 0 {
		gap = 100 * time.Millisecond
	}
	for i, s := range cfg.Send {
		if i > 0 {
			select {
			case <-time.After(gap):
			case <-ctx.Done():
				return st, nil
			}
		}
		if _, err := port.Write(s.Frame()); err != nil {
			return st, fmt.Errorf("hippo: sending %s: %w", s.Name, err)
		}
		logf("tx %s: % x", s.Name, s.Frame())
	}

	var timeout <-chan time.Time
	if cfg.Wait > 0 {
		timeout = time.After(cfg.Wait)
	}
	for {
		select {
		case <-ctx.Done():
			return st.with(&dec), nil
		case <-timeout:
			return st.with(&dec), nil
		case c := <-reads:
			for _, f := range dec.Write(c.b) {
				st.Frames++
				if !f.Valid {
					st.BadSum++
					if cfg.Dump != nil {
						fmt.Fprintf(cfg.Dump, "rx %v\n", f)
					}
					continue
				}
				m := Parse(f.Body)
				if cfg.Dump != nil {
					fmt.Fprintf(cfg.Dump, "rx %s  # %s\n", Hex(f.Body), m.Describe())
				}
				for _, s := range conv.Feed(m) {
					st.Sentences++
					if cfg.Out != nil {
						io.WriteString(cfg.Out, s)
					}
				}
			}
			if c.err != nil {
				if c.err == io.EOF || ctx.Err() != nil {
					return st.with(&dec), nil
				}
				return st.with(&dec), c.err
			}
		}
	}
}

func (s Stats) with(d *Decoder) Stats {
	s.Garbage, s.Dropped = d.Garbage(), d.Dropped()
	return s
}
