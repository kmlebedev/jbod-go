// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

func cmdList(ctx context.Context, args []string, out io.Writer, inv Inventory) error {
	f := flags("list", out)
	// One registration per option: pflag groups short flags itself, so
	// "-ed" and "-ef" need no expansion of their own (E).
	enc := f.BoolP("enclosure", "e", false, "list enclosures")
	disks := f.BoolP("disks", "d", false, "list enclosures with their disks")
	fans := f.BoolP("fan", "f", false, "list cooling elements")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("list takes no arguments, got %q", f.Arg(0))
	}
	if !*enc && !*disks && !*fans {
		return errors.New("list requires --enclosure, --disks or --fan")
	}
	enclosures, err := inv.Enclosures(ctx)
	if err != nil {
		return err
	}
	// Each flag selects an independent section: -e -f must print both.
	// Sections are flushed separately so tabwriter does not align the
	// enclosure columns against the fan columns.
	printed := false
	if *enc || *disks {
		var ds []jbod.Disk
		if *disks {
			ds, err = inv.Disks(ctx, enclosures, jbod.DiskOptions{WithTelemetry: true})
			if err != nil {
				return err
			}
		}
		if err := printEnclosures(out, enclosures, ds); err != nil {
			return err
		}
		printed = true
	}
	if *fans {
		fs, err := inv.Fans(ctx, enclosures)
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
}
