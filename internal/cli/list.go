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
	// listAliases, not enclosureAliases: in this command --enclosure is the
	// section switch it has always been, so the selector keeps its own
	// name and only the plural spellings are folded in.
	f.SetNormalizeFunc(listAliases)
	// One registration per option: pflag groups short flags itself, so
	// "-ed" and "-ef" need no expansion of their own (E).
	enc := f.BoolP("enclosure", "e", false, "list enclosures")
	disks := f.BoolP("disks", "d", false, "list enclosures with their disks")
	fans := f.BoolP("fan", "f", false, "list cooling elements")
	slots := f.BoolP("slots", "s", false, "list every slot, empty ones included")
	// The selector is a separate name because --enclosure is already the
	// section switch above; enclosureAliases makes --enclosure mean this
	// one in the commands that have no such clash.
	id := f.String("enclosure-id", "", "limit the listing to one shelf, by identifier, serial or SCSI address")
	asJSON := f.Bool("json", false, "print the inventory as JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("list takes no arguments, got %q", f.Arg(0))
	}
	if !*enc && !*disks && !*fans && !*slots {
		return errors.New("list requires --enclosure, --disks, --slots or --fan")
	}
	// One clear message about missing tools or an unusable sysfs tree,
	// before a single command runs (A4).
	if err := inv.Preflight(); err != nil {
		return err
	}
	all, err := inv.Enclosures(ctx)
	if err != nil {
		return err
	}
	enclosures, err := jbod.SelectEnclosures(all, *id)
	if err != nil {
		return err
	}
	var document listDocument
	var ds []jbod.Disk
	var ss []jbod.Slot
	var fs []jbod.Fan
	if *enc || *disks {
		document.Enclosures = section(enclosures)
	}
	if *disks {
		if ds, err = inv.Disks(ctx, enclosures, jbod.DiskOptions{WithTelemetry: true}); err != nil {
			return err
		}
		document.Disks = section(ds)
	}
	if *slots {
		if ss, err = inv.Slots(ctx, enclosures); err != nil {
			return err
		}
		document.Slots = section(ss)
	}
	if *fans {
		if fs, err = inv.Fans(ctx, enclosures); err != nil {
			return err
		}
		document.Fans = section(fs)
	}
	if *asJSON {
		return writeJSON(out, document)
	}
	// Each flag selects an independent section: -e -f must print both.
	// Sections are flushed separately so tabwriter does not align the
	// columns of one table against another.
	sections := []func() error{}
	if *enc || *disks {
		sections = append(sections, func() error { return printEnclosures(out, enclosures, ds) })
	}
	if *slots {
		sections = append(sections, func() error { return printSlots(out, enclosures, ss) })
	}
	if *fans {
		sections = append(sections, func() error { return printFans(out, fs) })
	}
	for i, section := range sections {
		if i > 0 {
			fmt.Fprintln(out)
		}
		if err := section(); err != nil {
			return err
		}
	}
	return nil
}
