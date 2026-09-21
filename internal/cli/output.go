// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"fmt"
	"io"
	"strconv"
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
	for _, e := range enclosures {
		fmt.Fprintln(w, "SLOT\tDEVICE\tVENDOR\tMODEL\tREVISION\tSERIAL")
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", fan.Slot, fan.Index, fan.Description, fan.Comment.Or(noFanCondition), fan.Speed)
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
		id, stable := e.Ref()
		note := "temporary, the shelf reports no stable identifier"
		if stable {
			note = e.IDSource()
		}
		fmt.Fprintf(out, "Enclosure %s  id %s (%s)\n", e.Slot, id, note)
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SLOT\tNAME\tTYPE\tSTATUS\tOCCUPANCY\tDEVICE\tMAP\tLOCATE\tFAULT\tPOWER")
		for _, s := range slots {
			if s.Enclosure != e.Slot {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				slotNumber(s), s.Name, s.Type.Or(noAttribute), s.Status.Or(noAttribute),
				s.Occupancy, s.Device.Or(noDevice), s.Map.Or(noDevice),
				boolCell(s.Locate), faultCell(s.Fault), s.Power.Or(noAttribute))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return nil
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
		fmt.Fprintf(out, "Enclosure %s  address %s (%s)  components %d\n",
			report.Enclosure, report.Address, stability, report.Components)
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
