// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// cmdCapabilities reports what each shelf can do, read and write judged
// separately, with the evidence behind each verdict.
//
// It performs no writes: discovery that changes the hardware to find out
// whether it can be changed is not discovery (ROADMAP 3). That is why no
// capability is ever reported as a supported write here; the led command
// confirms one by reading the indicator back after a real write.
func cmdCapabilities(ctx context.Context, args []string, out io.Writer, inv Inventory) error {
	f := flags("capabilities", out)
	f.SetNormalizeFunc(enclosureAliases)
	id := f.String("enclosure-id", "", "limit the report to one shelf, by identifier, serial or SCSI address")
	asJSON := f.Bool("json", false, "print the report as JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("capabilities takes no arguments, got %q", f.Arg(0))
	}
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
	reports, err := inv.Capabilities(ctx, enclosures)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(out, capabilitiesDocument{Enclosures: emptyToSlice(reports)})
	}
	return printCapabilities(out, reports)
}
