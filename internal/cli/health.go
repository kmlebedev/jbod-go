// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"context"
	"io"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// The two reports of v1.2, on top of one inspection pass: health is the
// verdict, sensors are the numbers behind it, and "list --components" is
// every element the shelf declares (ROADMAP 5).
//
// All three print what the enclosure reported and what could not be read as
// two different things. A shelf whose threshold page is not implemented is
// not a shelf in trouble, and a shelf whose status page did not answer is
// not a healthy one.

// cmdHealth reports the condition of each shelf: what the enclosure says
// about itself, what its elements report, and how complete the poll was.
func cmdHealth(ctx context.Context, args []string, out io.Writer, inv Inventory) error {
	statuses, asJSON, err := inspectCommand(ctx, "health", args,
		"limit the report to one shelf, by id, serial, SCSI address or device", out, inv)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(out, healthDocument{Enclosures: emptyToSlice(statuses)})
	}
	return printHealth(out, statuses)
}

// cmdSensors prints the values the enclosure's sensors report, with the
// thresholds it declares for them.
func cmdSensors(ctx context.Context, args []string, out io.Writer, inv Inventory) error {
	statuses, asJSON, err := inspectCommand(ctx, "sensors", args,
		"limit the listing to one shelf, by id, serial, SCSI address or device", out, inv)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(out, sensorsDocument{Enclosures: sensorSections(statuses)})
	}
	return printSensors(out, statuses)
}

// inspectCommand is the body both commands share: the same flags, the same
// shelf selection, and one inspection pass.
func inspectCommand(ctx context.Context, name string, args []string, usage string,
	out io.Writer, inv Inventory,
) ([]jbod.EnclosureStatus, bool, error) {
	f := flags(name, out)
	f.SetNormalizeFunc(enclosureAliases)
	id := f.String("enclosure-id", "", usage)
	asJSON := f.Bool("json", false, "print the report as JSON")
	if err := f.Parse(args); err != nil {
		return nil, false, err
	}
	selector, err := oneEnclosure(f, name, *id)
	if err != nil {
		return nil, false, err
	}
	if err := inv.Preflight(); err != nil {
		return nil, false, err
	}
	all, err := inv.Enclosures(ctx)
	if err != nil {
		return nil, false, err
	}
	enclosures, err := jbod.SelectEnclosures(all, selector)
	if err != nil {
		return nil, false, err
	}
	statuses, err := inv.Inspect(ctx, enclosures)
	if err != nil {
		return nil, false, err
	}
	return statuses, *asJSON, nil
}

// sensorSection is one shelf's sensors in the JSON document.
type sensorSection struct {
	Enclosure   string                `json:"enclosure"`
	EnclosureID jbod.Optional[string] `json:"enclosure_id"`
	Address     string                `json:"address"`
	Sensors     []jbod.Component      `json:"sensors"`
	Collection  jbod.CollectionStatus `json:"collection"`
}

// sensorSections projects the inspection onto the sensor view. The
// collection status travels with it: a listing of three sensors means
// something different when the page that would have carried the other five
// did not answer.
func sensorSections(statuses []jbod.EnclosureStatus) []sensorSection {
	sections := make([]sensorSection, 0, len(statuses))
	for _, status := range statuses {
		sections = append(sections, sensorSection{
			Enclosure:   status.Enclosure,
			EnclosureID: status.EnclosureID,
			Address:     status.Address,
			Sensors:     emptyToSlice(status.Sensors()),
			Collection:  status.Collection,
		})
	}
	return sections
}
