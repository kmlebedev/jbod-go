// SPDX-License-Identifier: BSD-2-Clause
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"jbod-go/internal/jbod"
)

const Help = `jbod - Generic storage enclosure tool (Go)
Usage:
  jbod list [-e|--enclosure] [-d|--disks] [-f|--fan]
  jbod led [-l|--locate DEVICE] [-f|--fault DEVICE] --on|--off
  jbod prometheus [-i|--ip-address IP] [-p|--port PORT] [tuning flags]

The exporter runs in the foreground (default 127.0.0.1:9945);
see "jbod prometheus --help" for the tuning flags.
`

const ExporterHelp = `prometheus-jbod-exporter - Prometheus exporter for storage enclosures (Go)
Usage:
  prometheus-jbod-exporter [-i|--ip-address IP] [-p|--port PORT] [tuning flags]
  prometheus-jbod-exporter IP PORT

Runs in the foreground (default 127.0.0.1:9945); GET /metrics serves the metrics.

Tuning flags:
  --command-timeout DUR  timeout of one external command (default 15s)
  --scrape-timeout DUR   timeout of one full collection (default 2m0s)
  --concurrency N        external commands allowed to run at once (default 12)
  --cache-ttl DUR        reuse the previous collection for this long (default 0s)

Concurrent scrapes always share one collection pass; --cache-ttl additionally
serves a recent result without touching the hardware. Set it to about half the
Prometheus scrape_interval if the shelf is polled from several places.
`

// Version is the single source of truth for both binaries. Keep it in sync
// with debian/control until the build injects it via -ldflags.
const Version = "1.0.0"

func flags(name string, w io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(w)
	return f
}
func boolean(f *flag.FlagSet, p *bool, short, long string) {
	f.BoolVar(p, short, false, long)
	f.BoolVar(p, long, false, long)
}

func Run(ctx context.Context, args []string, out io.Writer, c *jbod.Client) error {
	if len(args) == 0 {
		fmt.Fprint(out, Help)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(out, Help)
		return nil
	case "--version", "-V":
		fmt.Fprintln(out, "jbod-go "+Version)
		return nil
	case "list":
		f := flags("list", out)
		var enc, disks, fans bool
		boolean(f, &enc, "e", "enclosure")
		boolean(f, &disks, "d", "disks")
		boolean(f, &fans, "f", "fan")
		// clap-compatible combined short flags, e.g. -ed.
		var expanded []string
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 2 && strings.Trim(a[1:], "edf") == "" {
				for _, ch := range a[1:] {
					expanded = append(expanded, "-"+string(ch))
				}
			} else {
				expanded = append(expanded, a)
			}
		}
		if err := f.Parse(expanded); err != nil {
			return err
		}
		if f.NArg() != 0 || (!enc && !disks && !fans) {
			return errors.New("list requires --enclosure, --disks or --fan")
		}
		enclosures, err := c.Enclosures(ctx)
		if err != nil {
			return err
		}
		// Each flag selects an independent section: -e -f must print both.
		// Sections are flushed separately so tabwriter does not align the
		// enclosure columns against the fan columns.
		printed := false
		if enc || disks {
			var ds []jbod.Disk
			if disks {
				ds, err = c.Disks(ctx, enclosures, true)
				if err != nil {
					return err
				}
			}
			if err := printEnclosures(out, enclosures, ds); err != nil {
				return err
			}
			printed = true
		}
		if fans {
			fs, err := c.Fans(ctx, enclosures)
			if err != nil {
				return err
			}
			if printed {
				fmt.Fprintln(out)
			}
			if err := printFans(out, fs); err != nil {
				return err
			}
		}
		return nil
	case "led":
		f := flags("led", out)
		var locate, fault devices
		var on, off bool
		f.Var(&locate, "l", "locate device")
		f.Var(&locate, "locate", "locate device")
		f.Var(&fault, "f", "fault device")
		f.Var(&fault, "fault", "fault device")
		f.BoolVar(&on, "on", false, "turn on")
		f.BoolVar(&off, "off", false, "turn off")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || on == off || len(locate)+len(fault) == 0 {
			return errors.New("led requires device(s) and exactly one of --on or --off")
		}
		enc, err := c.Enclosures(ctx)
		if err != nil {
			return err
		}
		ds, err := c.Disks(ctx, enc, false)
		if err != nil {
			return err
		}
		for _, group := range []struct {
			kind    string
			targets devices
		}{{"locate", locate}, {"fault", fault}} {
			for _, device := range group.targets {
				if err := jbod.SetLED(ds, device, group.kind, on); err != nil {
					return err
				}
				fmt.Fprintf(out, "%s %s: %t\n", device, group.kind, on)
			}
		}
		return nil
	case "prometheus":
		return Exporter(ctx, args[1:], out, c)
	default:
		return fmt.Errorf("unknown command %q; use jbod help", args[0])
	}
}

func printEnclosures(out io.Writer, enclosures []jbod.Enclosure, ds []jbod.Disk) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, e := range enclosures {
		fmt.Fprintln(w, "SLOT\tDEVICE\tVENDOR\tMODEL\tREVISION\tSERIAL")
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Slot, e.Device, e.Vendor, e.Model, e.Revision, e.Serial)
		for _, d := range ds {
			if d.Enclosure == e.Slot {
				fmt.Fprintf(w, "  Disk: %s\tMap: %s\tSlot: %s\tVendor: %s\tModel: %s\tSerial: %s\tTemp: %s\tFw: %s\n", d.Device, d.Map, d.Slot, d.Vendor, d.Model, d.Serial, d.Temperature, d.Firmware)
			}
		}
	}
	return w.Flush()
}

func printFans(out io.Writer, fans []jbod.Fan) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SLOT\tIDENT\tDESCRIPTION\tSTATUS\tRPM")
	for _, fan := range fans {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", fan.Slot, fan.Index, fan.Description, fan.Comment, fan.Speed)
	}
	return w.Flush()
}

type devices []string

func (d *devices) String() string { return strings.Join(*d, ",") }
func (d *devices) Set(v string) error {
	if !strings.HasPrefix(v, "/dev/") {
		return errors.New("device must start with /dev/")
	}
	*d = append(*d, v)
	return nil
}

// Exporter listen defaults and derived timeouts.
const (
	// DefaultListenIP is the loopback address on purpose: the exporter has
	// access to /dev/sg* and normally runs as root, so exposing it to the
	// whole network should be a deliberate choice (B6).
	DefaultListenIP   = "127.0.0.1"
	DefaultListenPort = "9945"
	// shutdownTimeout bounds the graceful stop after SIGINT/SIGTERM.
	shutdownTimeout = 5 * time.Second
	// writeGrace is how much longer than a full collection a response may
	// take before the write times out.
	writeGrace = 10 * time.Second
)

// wildcardWarning returns a warning for a wildcard bind address, or "" for a
// specific one. A process that can read /dev/sg* and write LEDs should not
// reach the whole network by accident (B6).
func wildcardWarning(ip string) string {
	if parsed := net.ParseIP(ip); parsed == nil || !parsed.IsUnspecified() {
		return ""
	}
	return fmt.Sprintf("==> Warning: %s exposes enclosure telemetry on every interface; bind %s unless that is intended", ip, DefaultListenIP)
}

func Exporter(ctx context.Context, args []string, out io.Writer, c *jbod.Client) error {
	// The standalone binary never reaches Run, so it needs its own
	// --help/--version handling.
	if len(args) == 1 {
		switch args[0] {
		case "help", "-h", "--help":
			fmt.Fprint(out, ExporterHelp)
			return nil
		case "-V", "--version":
			fmt.Fprintln(out, "jbod-go "+Version)
			return nil
		}
	}
	f := flags("prometheus", out)
	ip, port := DefaultListenIP, DefaultListenPort
	for _, key := range []string{"i", "ip", "ip-address"} {
		f.StringVar(&ip, key, ip, "listen IP")
	}
	for _, key := range []string{"p", "port"} {
		f.StringVar(&port, key, port, "listen port")
	}
	// Timeouts and the concurrency limit are operational knobs: what fits a
	// 12-slot shelf is not what fits a 60-slot one, and a README advising
	// "raise scrape_timeout" needs a matching flag on this side (B4).
	commandTimeout := jbod.DefaultCommandTimeout
	if c.CommandTimeout > 0 {
		commandTimeout = c.CommandTimeout
	}
	concurrency := jbod.DefaultConcurrency
	if c.Concurrency > 0 {
		concurrency = c.Concurrency
	}
	scrapeTimeout := jbod.DefaultScrapeTimeout
	var cacheTTL time.Duration
	f.DurationVar(&commandTimeout, "command-timeout", commandTimeout, "timeout of one external command")
	f.DurationVar(&scrapeTimeout, "scrape-timeout", scrapeTimeout, "timeout of one full collection")
	f.DurationVar(&cacheTTL, "cache-ttl", cacheTTL, "reuse the previous collection for this long")
	f.IntVar(&concurrency, "concurrency", concurrency, "external commands allowed to run at once")
	// Keep standalone exporter's positional IP PORT invocation.
	if len(args) == 2 && !strings.HasPrefix(args[0], "-") {
		ip, port = args[0], args[1]
	} else {
		if err := f.Parse(args); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return errors.New("unexpected exporter arguments")
		}
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP address %q", ip)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid port %q", port)
	}
	if commandTimeout <= 0 {
		return fmt.Errorf("invalid command timeout %s", commandTimeout)
	}
	if scrapeTimeout <= 0 {
		return fmt.Errorf("invalid scrape timeout %s", scrapeTimeout)
	}
	if cacheTTL < 0 {
		return fmt.Errorf("invalid cache TTL %s", cacheTTL)
	}
	if concurrency < 1 {
		return fmt.Errorf("invalid concurrency %d", concurrency)
	}
	c.CommandTimeout = commandTimeout
	c.Concurrency = concurrency
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, port))
	if err != nil {
		return err
	}
	if warning := wildcardWarning(ip); warning != "" {
		fmt.Fprintln(out, warning)
	}
	server := &http.Server{
		Handler:           jbod.NewExporter(c, scrapeTimeout, cacheTTL).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		WriteTimeout:      scrapeTimeout + writeGrace,
		// Without BaseContext a scrape in flight only sees the cancellation
		// when the connection is closed, so SIGTERM would wait out the whole
		// shutdown timeout with sg_ses still running (B3).
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := server.Shutdown(shutdown); err != nil {
				server.Close()
			}
		case <-done:
		}
	}()
	fmt.Fprintf(out, "==> Started on %s\n", listener.Addr())
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
