// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// Sentinels for readings the hardware did not report. Rendering absence is
// the output layer's job; the collectors leave the value absent (C1).
const (
	// unknownValue is what the Rust original prints for a missing text
	// field, noTemperature for a temperature that could not be read,
	// noMapping for a slot without a block device and noIdentity for a
	// shelf that did not answer sg_inq.
	unknownValue   = "N/A"
	noTemperature  = "ERR"
	noMapping      = "NONE"
	noIdentity     = "NONE"
	noFanCondition = ""
)

func printEnclosures(out io.Writer, enclosures []jbod.Enclosure, ds []jbod.Disk) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	// One header for the table, not one per shelf: a chassis with two I/O
	// modules is two enclosures, and repeating the header between them
	// reads as two separate tables.
	fmt.Fprintln(w, "SLOT\tDEVICE\tVENDOR\tMODEL\tREVISION\tSERIAL")
	for _, e := range enclosures {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Slot, e.Device,
			e.Vendor.Or(noIdentity), e.Model.Or(noIdentity), e.Revision.Or(noIdentity), e.Serial.Or(noIdentity))
		for _, d := range ds {
			if d.Enclosure != e.Slot {
				continue
			}
			fmt.Fprintf(w, "  Disk: %s\tMap: %s\tSlot: %s\tVendor: %s\tModel: %s\tSerial: %s\tTemp: %s\tFw: %s\n",
				d.Device, d.Map.Or(noMapping), d.Slot, d.Vendor.Or(unknownValue), d.Model.Or(unknownValue),
				d.Serial.Or(unknownValue), temperature(d), d.Firmware.Or(unknownValue))
		}
	}
	return w.Flush()
}

// rpm renders a fan speed. An element that answered without a reading
// prints a dash: printing 0 would be indistinguishable from a stopped fan.
func rpm(f jbod.Fan) string {
	n, ok := f.Speed.Get()
	if !ok {
		return noAttribute
	}
	return strconv.FormatInt(n, 10)
}

// temperature renders a disk temperature in degrees Celsius.
func temperature(d jbod.Disk) string {
	n, ok := d.Temperature.Get()
	if !ok {
		return noTemperature
	}
	return strconv.FormatInt(n, 10)
}

func printFans(out io.Writer, fans []jbod.Fan) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SLOT\tIDENT\tDESCRIPTION\tSTATUS\tRPM")
	for _, fan := range fans {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", fan.Slot, fan.Index, fan.Description,
			fan.Comment.Or(noFanCondition), rpm(fan))
	}
	return w.Flush()
}

// Sentinels for the slot table. A slot that has no such attribute is not a
// slot whose value is zero, and the two must not render the same (ROADMAP 3).
const (
	// noAttribute is what an attribute the enclosure does not expose looks
	// like, and noDevice a bay with nothing in it.
	noAttribute = "-"
	noDevice    = "-"
)

// printSlots renders every bay of each shelf, empty ones included.
//
// The header line carries the stable identifier and says where it came
// from, because a listing keyed only by the SCSI address is a listing keyed
// by something that changes at the next reboot (ROADMAP 3).
func printSlots(out io.Writer, enclosures []jbod.Enclosure, slots []jbod.Slot) error {
	for i, e := range enclosures {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, enclosureHeading(enclosures, i))
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SLOT\tNAME\tTYPE\tSTATUS\tOCCUPANCY\tDEVICE\tMAP\tLOCATE\tFAULT\tPOWER")
		var reasons []string
		counts := map[string]int{}
		for _, s := range slots {
			if s.Enclosure != e.Slot {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				slotNumber(s), s.Name, s.Type.Or(noAttribute), s.Status.Or(noAttribute),
				s.Occupancy, s.Device.Or(noDevice), s.Map.Or(noDevice),
				boolCell(s.Locate), faultCell(s.Fault), s.Power.Or(noAttribute))
			if reason, ok := s.Err.Get(); ok {
				if counts[reason] == 0 {
					reasons = append(reasons, reason)
				}
				counts[reason]++
			}
		}
		if err := w.Flush(); err != nil {
			return err
		}
		// Thirty rows reading "unavailable" with no explanation is not an
		// answer. The reasons are collected rather than repeated per row,
		// because on a two-module chassis they are all the same reason.
		for _, reason := range reasons {
			fmt.Fprintf(out, "note: %d slot(s): %s\n", counts[reason], reason)
		}
	}
	return nil
}

// enclosureHeading names one shelf: the sysfs enclosure, the identifier to
// address it by, where that identifier came from, and the other sysfs
// enclosures that answer to it.
func enclosureHeading(enclosures []jbod.Enclosure, i int) string {
	e := enclosures[i]
	id, stable := e.Ref()
	source := e.IDSource()
	if !stable {
		source = "temporary, the shelf reports no stable identifier"
	}
	heading := fmt.Sprintf("Enclosure %s  id %s (%s)", e.Slot, id, source)
	siblings := jbod.SiblingsOf(enclosures, i)
	if len(siblings) == 0 {
		return heading
	}
	paths := make([]string, len(siblings))
	for j, sibling := range siblings {
		paths[j] = sibling.Slot
	}
	// Same identifier, different sysfs enclosure: one chassis reached
	// through more than one I/O module. Saying so here is what keeps the
	// duplicated slot list from reading as two shelves.
	return heading + fmt.Sprintf("  (same chassis as %s)", strings.Join(paths, ", "))
}

// slotNumber renders the addressable slot number, falling back to the
// component name when the enclosure reports no number.
func slotNumber(s jbod.Slot) string {
	if n, ok := s.Number.Get(); ok {
		return strconv.FormatInt(n, 10)
	}
	return noAttribute
}

// boolCell renders an indicator that may not exist at all.
func boolCell(v jbod.Optional[bool]) string {
	b, ok := v.Get()
	if !ok {
		return noAttribute
	}
	if b {
		return "on"
	}
	return "off"
}

// faultCell renders the two halves of the fault indication.
//
// "sensed" is the enclosure reporting a fault it detected; "requested" is a
// fault LED somebody switched on. Collapsing them into one column would
// turn an operator's marker into a hardware alarm (ROADMAP 4).
func faultCell(f jbod.FaultState) string {
	if !f.Value.Present() {
		return noAttribute
	}
	sensed, requested := f.Sensed.Or(false), f.Requested.Or(false)
	switch {
	case sensed && requested:
		return "sensed+requested"
	case sensed:
		return "sensed"
	case requested:
		return "requested"
	default:
		return "off"
	}
}

// printCapabilities renders the capability report of each shelf.
func printCapabilities(out io.Writer, reports []jbod.EnclosureCapabilities) error {
	for i, report := range reports {
		if i > 0 {
			fmt.Fprintln(out)
		}
		stability := "temporary"
		if report.StableID {
			stability = "stable"
		}
		shared := ""
		if len(report.Shared) > 0 {
			shared = "  (same chassis as " + strings.Join(report.Shared, ", ") + ")"
		}
		fmt.Fprintf(out, "Enclosure %s  address %s (%s)  components %d%s\n",
			report.Enclosure, report.Address, stability, report.Components, shared)
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "CAPABILITY\tREAD\tWRITE\tEVIDENCE")
		for _, c := range report.Capabilities {
			evidence := c.Evidence
			if err, ok := c.Err.Get(); ok {
				// The probe failure is appended rather than replacing the
				// verdict: "we could not find out" is not "it cannot".
				evidence += " [error: " + err + "]"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Name, c.Read, c.Write, evidence)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return nil
}
