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
