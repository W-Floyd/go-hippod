package hippo

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Main is the hippod command: it reads the GNSS module's hippo frames and
// writes NMEA. args are os.Args[1:]. It returns the exit code and writes only
// to the writers it is given, so a larger program can embed it.
//
//	hippod -dev /dev/ttyAMA3 [-baud 115200] [-out PATH|-|file:PATH] [-dump] [-query] [-replay-stock]
//
// By default it only reads. -query sends the four plain queries (version,
// output protocol mask, constellation, DR rate) once, prints what comes back
// for -wait and exits. The only frames that change the module are the two set
// frames of -replay-stock, which are what the stock HAL sends on a unit whose
// protocol mask is not 3; nothing else is ever composed.
//
// -tap SOCKET lets another process watch the frames of a running hippod, which
// owns the UART: hippod -attach SOCKET prints them, one line each with the
// time, until it is stopped.
//
// -gpsd adds $GPGSA (fix mode, used satellites, DOPs) and $GPGST (accuracy) to
// the output and thins -out to one GGA/RMC/VTG/GSA/GST set per UTC second, which
// is what gpsd wants; without it -out is as it always was.
//
// -nmea-tcp ADDR serves the same NMEA to any number of clients on a TCP
// host:port (or unix:PATH), thinned to one GGA/RMC/VTG set per UTC second, which
// is what GeoClue's network-nmea source wants; -out is unchanged.
//
// -out "-" is stdout (and then -dump goes to stderr); a path on Linux becomes
// a pty whose slave end gpsd can open (the path is a symlink to it); file:PATH
// appends to a file.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hippod", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dev := fs.String("dev", "", "the GNSS module's serial device (the vendor's /dev/ttyTCC3, a PL011 at 0x16630000; /dev/ttyAMA3 on mainline)")
	baud := fs.Int("baud", 115200, "baud rate; the stock HAL opens the port at 115200")
	out := fs.String("out", "-", `where NMEA goes: "-" for stdout, file:PATH to append to a file, else a pty symlink at PATH (Linux)`)
	dump := fs.Bool("dump", false, "print every frame with its decoded fields")
	query := fs.Bool("query", false, "send the four safe query frames once, print the replies for -wait, and exit")
	replay := fs.Bool("replay-stock", false, "send the two set frames the stock HAL sends (NMEA control, protocol mask 3); changes the module")
	wait := fs.Duration("wait", 3*time.Second, "how long -query listens for replies")
	altDR := fs.Bool("alt-dr", false, "take the GGA altitude from the DR message when the module sends no 0x70/0x22 (a guess)")
	tapPath := fs.String("tap", "", "serve every frame with its decoded fields, with the time, on this unix socket, for -attach (not with -query)")
	gpsd := fs.Bool("gpsd", false, "better for gpsd: add $GPGSA and $GPGST, and write one position set per UTC second to -out")
	nmeaNet := fs.String("nmea-tcp", "", "also serve the NMEA, one position set per UTC second, on this TCP host:port or unix:PATH, for GeoClue (not with -query)")
	attach := fs.String("attach", "", "print the frames of the hippod serving this -tap socket, instead of running one")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *attach != "" {
		if err := Attach(*attach, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if *dev == "" {
		fs.Usage()
		return 2
	}
	lg := log.New(stderr, "", log.LstdFlags)

	cfg := RunConfig{AltFromDR: *altDR, Extra: *gpsd, Logf: lg.Printf}
	if *dump {
		cfg.Dump = stdout
		if *out == "-" {
			cfg.Dump = stderr
		}
	}
	if *tapPath != "" && !*query {
		tap, err := ListenTap(*tapPath)
		if err != nil {
			lg.Print(err)
			return 1
		}
		defer tap.Close()
		if cfg.Dump == nil {
			cfg.Dump = stamped{tap}
		} else {
			cfg.Dump = io.MultiWriter(cfg.Dump, stamped{tap})
		}
	}
	if *query {
		cfg.Send = append(cfg.Send, SafeQueries()...)
		cfg.Wait = *wait
		cfg.Out = nil
		if cfg.Dump == nil {
			cfg.Dump = stdout
		}
	} else {
		w, closeOut, err := openOut(*out, stdout)
		if err != nil {
			lg.Print(err)
			return 1
		}
		defer closeOut()
		cfg.Out = w
		if *gpsd {
			cfg.Out = &ThinWriter{W: w}
		}
		if *nmeaNet != "" {
			srv, err := ListenNMEA(*nmeaNet)
			if err != nil {
				lg.Print(err)
				return 1
			}
			defer srv.Close()
			cfg.Out = teeWriter{cfg.Out, srv}
		}
	}
	if *replay {
		cfg.Send = append(cfg.Send, ReplayStock()...)
	}

	port, err := OpenTTY(*dev, *baud)
	if err != nil {
		lg.Print(err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := Run(ctx, port, cfg)
	lg.Print(st)
	if err != nil && ctx.Err() == nil {
		lg.Print(err)
		return 1
	}
	return 0
}

func openOut(spec string, stdout io.Writer) (io.Writer, func(), error) {
	switch {
	case spec == "-":
		return stdout, func() {}, nil
	case len(spec) > 5 && spec[:5] == "file:":
		f, err := os.OpenFile(spec[5:], os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return nil, nil, err
		}
		return f, func() { f.Close() }, nil
	}
	p, err := OpenPTY(spec)
	if err != nil {
		return nil, nil, fmt.Errorf("hippo: -out %s: %w", spec, err)
	}
	return p, func() { p.Close() }, nil
}
