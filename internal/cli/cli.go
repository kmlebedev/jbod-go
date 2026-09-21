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
	"strings"

	"github.com/spf13/pflag"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// Help is the top-level usage of the jbod binary.
const Help = `jbod - Generic storage enclosure tool (Go)
Usage:
  jbod list [-e|--enclosure] [-d|--disks] [-f|--fan] [-s|--slots] [-c|--components] [ENCLOSURE] [--json]
  jbod capabilities [ENCLOSURE] [--json]
  jbod health [ENCLOSURE] [--json]
  jbod sensors [ENCLOSURE] [--json]
  jbod led [-l|--locate TARGET] [-f|--fault TARGET] --on|--off [--json]
  jbod prometheus [-i|--ip-address IP] [-p|--port PORT] [tuning flags]

  --slots lists every bay, empty ones included, and --components every SES
  element the shelf declares: bays, power supplies, fans, sensors and I/O
  modules, with the disk behind each bay.

  health reports the condition of a shelf and sensors the values behind it.
  Both separate what the enclosure reports from what could not be read: a
  page that did not answer is shown as a gap in the poll and never as a
  healthy component.

  ENCLOSURE narrows a listing to one shelf and is any of the four spellings
  the listings print: the logical identifier, the unit serial number, the
  SCSI address or the generic device. Two I/O modules of one chassis share
  the identifier and the serial, so the device is what tells them apart.
  "jbod list -e 0x5000..." and "jbod list --enclosure-id 0x5000..." are the
  same thing, and naming a shelf without a section lists that shelf.

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
	// Inspect reads the SES pages behind the health, component and sensor
	// reports: one pass per command, shared by all three (ROADMAP 5).
	Inspect(ctx context.Context, enclosures []jbod.Enclosure) ([]jbod.EnclosureStatus, error)
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

// listAliases accepts the plural spellings. "--fans" is what an operator
// types next to "--disks" and "--slots", and answering "unknown flag" to a
// reasonable guess is a small cruelty.
func listAliases(_ *pflag.FlagSet, name string) pflag.NormalizedName {
	switch name {
	case "fans":
		return "fan"
	case "enclosures":
		return "enclosure"
	case "disk":
		return "disks"
	case "slot":
		return "slots"
	}
	return pflag.NormalizedName(name)
}

// oneEnclosure resolves the shelf a command was pointed at.
//
// Every report takes the shelf either as the value of --enclosure-id or as
// its single operand, because the flag spelling is easy to reach for and a
// bare argument is what people type first. Naming it twice is an error
// rather than a silent winner.
func oneEnclosure(f *pflag.FlagSet, command, id string) (string, error) {
	switch f.NArg() {
	case 0:
		return id, nil
	case 1:
		if id != "" && !strings.EqualFold(id, f.Arg(0)) {
			return "", fmt.Errorf("the shelf is named twice, as %q and %q", id, f.Arg(0))
		}
		return f.Arg(0), nil
	default:
		return "", fmt.Errorf("%s takes at most one enclosure, got %d arguments", command, f.NArg())
	}
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
	case "health":
		return cmdHealth(ctx, args[1:], out, c)
	case "sensors":
		return cmdSensors(ctx, args[1:], out, c)
	case "led":
		return cmdLED(ctx, args[1:], out, c)
	case "prometheus":
		return Prometheus(ctx, args[1:], out, errOut, c)
	default:
		return fmt.Errorf("unknown command %q; use jbod help", args[0])
	}
}
