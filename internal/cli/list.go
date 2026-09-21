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
	// Components are the SES elements, which is a superset of the bays: a
	// power supply and a temperature sensor have no sysfs slot to be
	// listed as (ROADMAP 5).
	components := f.BoolP("components", "c", false, "list every SES element the enclosure declares")
	// The selector is a separate name because --enclosure is already the
	// section switch above; enclosureAliases makes --enclosure mean this
	// one in the commands that have no such clash.
	id := f.String("enclosure-id", "", "limit the listing to one shelf, by id, serial, SCSI address or device")
	asJSON := f.Bool("json", false, "print the inventory as JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	// The shelf may also be named positionally: "list -e 0x5000...". The
	// flag spelling is easy to reach for as if it were a section of its
	// own, and "jbod list --enclosure-id X" answering "list requires
	// --enclosure" helps nobody. A bare argument is unambiguous — list has
	// no other operand — and it is what people type first.
	selector, err := oneEnclosure(f, "list", *id)
	if err != nil {
		return err
	}
	if !*enc && !*disks && !*fans && !*slots && !*components {
		if selector == "" {
			return errors.New("list requires --enclosure, --disks, --slots, --components or --fan")
		}
		// Naming a shelf and nothing else means "show me that shelf".
		*enc = true
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
	enclosures, err := jbod.SelectEnclosures(all, selector)
	if err != nil {
		return err
	}
	var document listDocument
	var ds []jbod.Disk
	var ss []jbod.Slot
	var fs []jbod.Fan
	var statuses []jbod.EnclosureStatus
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
	if *components {
		if statuses, err = inv.Inspect(ctx, enclosures); err != nil {
			return err
		}
		document.Components = section(componentsOf(statuses))
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
	if *components {
		sections = append(sections, func() error { return printComponents(out, statuses) })
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

// componentsOf flattens the per-shelf reports into one component list for
// the JSON document, which is the shape the other sections have.
func componentsOf(statuses []jbod.EnclosureStatus) []jbod.Component {
	var result []jbod.Component
	for _, status := range statuses {
		result = append(result, status.Components...)
	}
	return result
}
