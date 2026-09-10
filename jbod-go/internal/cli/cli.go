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
  jbod prometheus [-i|--ip-address IP] [-p|--port PORT]

The exporter runs in the foreground (default 0.0.0.0:9945).
`

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
		fmt.Fprintln(out, "jbod-go 1.0.0")
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
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		if disks || enc {
			var ds []jbod.Disk
			if disks {
				ds, err = c.Disks(ctx, enclosures, true)
				if err != nil {
					return err
				}
			}
			for _, e := range enclosures {
				fmt.Fprintln(w, "SLOT\tDEVICE\tVENDOR\tMODEL\tREVISION\tSERIAL")
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Slot, e.Device, e.Vendor, e.Model, e.Revision, e.Serial)
				for _, d := range ds {
					if d.Enclosure == e.Slot {
						fmt.Fprintf(w, "  Disk: %s\tMap: %s\tSlot: %s\tVendor: %s\tModel: %s\tSerial: %s\tTemp: %s\tFw: %s\n", d.Device, d.Map, d.Slot, d.Vendor, d.Model, d.Serial, d.Temperature, d.Firmware)
					}
				}
			}
		} else {
			fs, err := c.Fans(ctx, enclosures)
			if err != nil {
				return err
			}
			fmt.Fprintln(w, "SLOT\tIDENT\tDESCRIPTION\tSTATUS\tRPM")
			for _, fan := range fs {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", fan.Slot, fan.Index, fan.Description, fan.Comment, fan.Speed)
			}
		}
		return w.Flush()
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

type devices []string

func (d *devices) String() string { return strings.Join(*d, ",") }
func (d *devices) Set(v string) error {
	if !strings.HasPrefix(v, "/dev/") {
		return errors.New("device must start with /dev/")
	}
	*d = append(*d, v)
	return nil
}

func Exporter(ctx context.Context, args []string, out io.Writer, c *jbod.Client) error {
	f := flags("prometheus", out)
	ip, port := "0.0.0.0", "9945"
	for _, key := range []string{"i", "ip", "ip-address"} {
		f.StringVar(&ip, key, ip, "listen IP")
	}
	for _, key := range []string{"p", "port"} {
		f.StringVar(&port, key, port, "listen port")
	}
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
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, port))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: c.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 130 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
