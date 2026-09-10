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
type devices []string

func (d *devices) String() string { return strings.Join(*d, ",") }

func (d *devices) Set(v string) error {
	if !strings.HasPrefix(v, "/dev/") {
		return errors.New("device must start with /dev/")
	}
	*d = append(*d, v)
	return nil
}

func cmdLED(ctx context.Context, args []string, out io.Writer, inv Inventory) error {
	f := flags("led", out)
	var locate, fault devices
	var on, off bool
	f.Var(&locate, "l", "locate device")
	f.Var(&locate, "locate", "locate device")
	f.Var(&fault, "f", "fault device")
	f.Var(&fault, "fault", "fault device")
	f.BoolVar(&on, "on", false, "turn on")
	f.BoolVar(&off, "off", false, "turn off")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || on == off || len(locate)+len(fault) == 0 {
		return errors.New("led requires device(s) and exactly one of --on or --off")
	}
	// The client resolves the sysfs attribute itself, so the CLI no longer
	// carries LED paths around in a disk listing (C3). Writes happen in
	// order and stop at the first failure; earlier ones are not rolled back.
	for _, group := range []struct {
		kind    jbod.LEDKind
		targets devices
	}{{jbod.LEDLocate, locate}, {jbod.LEDFault, fault}} {
		for _, device := range group.targets {
			if err := inv.SetLED(ctx, device, group.kind, on); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s: %t\n", device, group.kind, on)
		}
	}
	return nil
}
