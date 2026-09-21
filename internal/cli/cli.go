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
  jbod list [-e|--enclosure] [-d|--disks] [-f|--fan] [-s|--slots] [--json]
  jbod capabilities [--enclosure ID] [--json]
  jbod led [-l|--locate TARGET] [-f|--fault TARGET] --on|--off [--json]
  jbod prometheus [-i|--ip-address IP] [-p|--port PORT] [tuning flags]

  --slots lists every bay, empty ones included; --enclosure-id narrows any
  listing to one shelf, addressed by its logical identifier, its unit serial
  number or its SCSI address.

  A led TARGET is a device path as before (/dev/sda, /dev/sg1), or a slot
  written as ENCLOSURE/SLOT, or a bare SLOT together with --enclosure. A slot
  can be addressed even when it is empty.

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
	Slots(ctx context.Context, enclosures []jbod.Enclosure) ([]jbod.Slot, error)
	Disks(ctx context.Context, enclosures []jbod.Enclosure, opts jbod.DiskOptions) ([]jbod.Disk, error)
	Fans(ctx context.Context, enclosures []jbod.Enclosure) ([]jbod.Fan, error)
	Capabilities(ctx context.Context, enclosures []jbod.Enclosure) ([]jbod.EnclosureCapabilities, error)
	SetLED(ctx context.Context, target jbod.LEDTarget, kind jbod.LEDKind, on bool) (jbod.LEDResult, error)
}

// enclosureAliases lets --enclosure stand for --enclosure-id in the
// commands that select a shelf.
//
// The list command cannot use it: there --enclosure has always been the
// section switch, and a selector that quietly took its name would turn
// "jbod list --enclosure" into a parse error for every existing script. So
// the selector has a name of its own, --enclosure-id, which works
// everywhere, and the shorter spelling is accepted where it is free.
func enclosureAliases(_ *pflag.FlagSet, name string) pflag.NormalizedName {
	if name == "enclosure" {
		return "enclosure-id"
	}
	return pflag.NormalizedName(name)
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
	case "capabilities":
		return cmdCapabilities(ctx, args[1:], out, c)
	case "led":
		return cmdLED(ctx, args[1:], out, c)
	case "prometheus":
		return Prometheus(ctx, args[1:], out, errOut, c)
	default:
		return fmt.Errorf("unknown command %q; use jbod help", args[0])
	}
}
