// SPDX-License-Identifier: BSD-2-Clause

// Package cli parses the command line of both binaries and dispatches to the
// subcommands. Each command lives in its own file (list.go, led.go,
// prometheus.go) and the table rendering in output.go.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

const Help = `jbod - Generic storage enclosure tool (Go)
Usage:
  jbod list [-e|--enclosure] [-d|--disks] [-f|--fan]
  jbod led [-l|--locate DEVICE] [-f|--fault DEVICE] --on|--off
  jbod prometheus [-i|--ip-address IP] [-p|--port PORT] [tuning flags]

The exporter runs in the foreground (default 127.0.0.1:9945);
see "jbod prometheus --help" for the tuning flags.
`

// Version is the single source of truth for both binaries. Keep it in sync
// with debian/control until the build injects it via -ldflags.
const Version = "1.0.0"

// Inventory is the hardware access the list and led commands need. They take
// the interface rather than *jbod.Client so their tests can exercise the
// rendering without going through lsscsi output (C5).
type Inventory interface {
	Enclosures(ctx context.Context) ([]jbod.Enclosure, error)
	Disks(ctx context.Context, enclosures []jbod.Enclosure, opts jbod.DiskOptions) ([]jbod.Disk, error)
	Fans(ctx context.Context, enclosures []jbod.Enclosure) ([]jbod.Fan, error)
	SetLED(ctx context.Context, device string, kind jbod.LEDKind, on bool) error
}

func flags(name string, w io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(w)
	return f
}

func boolean(f *flag.FlagSet, p *bool, short, long string) {
	f.BoolVar(p, short, false, long)
	f.BoolVar(p, long, false, long)
}

// Run dispatches the jbod subcommands. It takes the concrete client because
// the exporter derives its own budgets from it; the commands themselves work
// against Inventory.
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
		return cmdList(ctx, args[1:], out, c)
	case "led":
		return cmdLED(ctx, args[1:], out, c)
	case "prometheus":
		return Prometheus(ctx, args[1:], out, c)
	default:
		return fmt.Errorf("unknown command %q; use jbod help", args[0])
	}
}
