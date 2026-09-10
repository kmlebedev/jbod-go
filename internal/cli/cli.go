// SPDX-License-Identifier: BSD-2-Clause

// Package cli parses the command line of both binaries and dispatches to the
// subcommands. Each command lives in its own file (list.go, led.go,
// prometheus.go), the table rendering in output.go and the shared process
// entry point in main.go.
//
// Options follow POSIX through spf13/pflag: short flags group (-ed), long
// flags take --flag=value, and -- ends the flags.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/pflag"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// Help is the top-level usage of the jbod binary.
const Help = `jbod - Generic storage enclosure tool (Go)
Usage:
  jbod list [-e|--enclosure] [-d|--disks] [-f|--fan]
  jbod led [-l|--locate DEVICE] [-f|--fault DEVICE] --on|--off
  jbod prometheus [-i|--ip-address IP] [-p|--port PORT] [tuning flags]

The exporter runs in the foreground (default 127.0.0.1:9945);
see "jbod prometheus --help" for the tuning flags.
`

// Inventory is the hardware access the list and led commands need. They take
// the interface rather than *jbod.Client so their tests can exercise the
// rendering without going through lsscsi output (C5).
type Inventory interface {
	// Preflight reports everything that is missing before any collection
	// is attempted.
	Preflight() error
	Enclosures(ctx context.Context) ([]jbod.Enclosure, error)
	Disks(ctx context.Context, enclosures []jbod.Enclosure, opts jbod.DiskOptions) ([]jbod.Disk, error)
	Fans(ctx context.Context, enclosures []jbod.Enclosure) ([]jbod.Fan, error)
	SetLED(ctx context.Context, device string, kind jbod.LEDKind, on bool) error
}

// flags returns a POSIX flag set that reports errors to the caller instead of
// exiting, and prints its usage where the command prints everything else.
func flags(name string, w io.Writer) *pflag.FlagSet {
	f := pflag.NewFlagSet(name, pflag.ContinueOnError)
	f.SetOutput(w)
	return f
}

// Run dispatches the jbod subcommands. It takes the concrete client because
// the exporter derives its own budgets and logger from it; the commands
// themselves work against Inventory.
func Run(ctx context.Context, args []string, out, errOut io.Writer, c *jbod.Client) error {
	if len(args) == 0 {
		fmt.Fprint(out, Help)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(out, Help)
		return nil
	case "--version", "-V":
		fmt.Fprintln(out, "jbod-go "+Version())
		return nil
	case "list":
		return cmdList(ctx, args[1:], out, c)
	case "led":
		return cmdLED(ctx, args[1:], out, c)
	case "prometheus":
		return Prometheus(ctx, args[1:], out, errOut, c)
	default:
		return fmt.Errorf("unknown command %q; use jbod help", args[0])
	}
}
