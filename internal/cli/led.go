// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// devices collects a repeatable device option: led -l /dev/sda -l /dev/sdb.
// It rejects anything that is not a device path at parse time, before any
// hardware is touched.
type devices []string

// String renders the collected devices for the usage message.
func (d *devices) String() string { return strings.Join(*d, ",") }

// Set appends one device, rejecting anything that is not a /dev/ path.
func (d *devices) Set(v string) error {
	if !strings.HasPrefix(v, "/dev/") {
		return errors.New("device must start with /dev/")
	}
	*d = append(*d, v)
	return nil
}

// Type names the value in the usage message.
func (d *devices) Type() string { return "device" }

func cmdLED(ctx context.Context, args []string, out io.Writer, inv Inventory) error {
	f := flags("led", out)
	var locate, fault devices
	f.VarP(&locate, "locate", "l", "device whose locate LED to switch")
	f.VarP(&fault, "fault", "f", "device whose fault LED to switch")
	on := f.Bool("on", false, "turn the LED on")
	off := f.Bool("off", false, "turn the LED off")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("led takes no arguments, got %q", f.Arg(0))
	}
	if *on == *off || len(locate)+len(fault) == 0 {
		return errors.New("led requires device(s) and exactly one of --on or --off")
	}
	if err := inv.Preflight(); err != nil {
		return err
	}
	// The client resolves the sysfs attribute itself, so the CLI no longer
	// carries LED paths around in a disk listing (C3). Writes happen in
	// order and stop at the first failure; earlier ones are not rolled back.
	for _, group := range []struct {
		kind    jbod.LEDKind
		targets devices
	}{{jbod.LEDLocate, locate}, {jbod.LEDFault, fault}} {
		for _, device := range group.targets {
			if err := inv.SetLED(ctx, device, group.kind, *on); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s: %t\n", device, group.kind, *on)
		}
	}
	return nil
}
