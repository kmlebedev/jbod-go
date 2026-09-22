// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"context"
	"io"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// The v1.3 connection report: what each SAS phy is, what rate it came up
// at, and what the hardware has counted against it (ROADMAP 6).
//
// It answers the question that the enclosure reports cannot: a shelf whose
// elements all read OK and whose disks keep timing out is a cable problem,
// and the only place that is visible is the link.

// cmdPHY prints the SAS phys of the hosts the shelves are attached through.
//
// The sysfs half costs no external command at all. SMP is opt-in with
// --smp, because one request per phy through one expander is a reasonable
// thing for an operator to ask for once and a bad thing to do on a timer.
func cmdPHY(ctx context.Context, args []string, out io.Writer, inv Inventory) error {
	f := flags("phy", out)
	f.SetNormalizeFunc(enclosureAliases)
	id := f.String("enclosure-id", "",
		"limit the report to the host of one shelf, by id, serial, SCSI address or device")
	smp := f.Bool("smp", false,
		"also ask every expander over SMP for its phys and their error counters (needs smp_utils; one request per phy)")
	asJSON := f.Bool("json", false, "print the report as JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	selector, err := oneEnclosure(f, "phy", *id)
	if err != nil {
		return err
	}
	if err := inv.Preflight(); err != nil {
		return err
	}
	all, err := inv.Enclosures(ctx)
	if err != nil {
		return err
	}
	// An empty selector means every host, so the enclosure list is only
	// narrowed when one was given: a machine whose phys are worth looking
	// at may have no enclosure the driver recognises at all.
	enclosures := all
	if selector != "" {
		if enclosures, err = jbod.SelectEnclosures(all, selector); err != nil {
			return err
		}
	}
	reports, err := inv.SAS(ctx, enclosures, jbod.SASOptions{WithSMP: *smp})
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(out, phyDocument{Hosts: emptyToSlice(reports)})
	}
	return printPHYs(out, reports)
}

// phyDocument is what "jbod phy --json" prints: one entry per SCSI host,
// each carrying its phys, its expanders and how complete the read was.
type phyDocument struct {
	Hosts []jbod.SASReport `json:"hosts"`
}
