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

// The v1.2 reports: the condition of a shelf, the elements behind it and
// the sensor values with their thresholds (ROADMAP 5).
//
// All three print the enclosure's own answers and the gaps in the poll as
// two different things, which is why the collection row exists and why a
// failed page becomes a note rather than an absent element.

// statusHeading names one shelf in the health, sensor and component
// reports, the way the capability report does.
func statusHeading(status jbod.EnclosureStatus) string {
	stability := "temporary"
	if status.StableID {
		stability = "stable"
	}
	return fmt.Sprintf("Enclosure %s  address %s (%s)", status.Enclosure, status.Address, stability)
}

// conditionBits renders the five bits of the Enclosure Status page. A bit
// the page did not report prints as "-", because a shelf nobody could ask
// is not a shelf that answered zero.
func conditionBits(h jbod.HardwareStatus) string {
	pairs := []struct {
		name  string
		value jbod.Optional[bool]
	}{
		{"INVOP", h.InvalidOperation}, {"INFO", h.Info}, {"NON-CRIT", h.NonCritical},
		{"CRIT", h.Critical}, {"UNRECOV", h.Unrecoverable},
	}
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		value := noAttribute
		if v, ok := pair.value.Get(); ok {
			value = "0"
			if v {
				value = "1"
			}
		}
		parts = append(parts, pair.name+"="+value)
	}
	return strings.Join(parts, " ")
}

// componentCounts renders the roll-up as "60 ok, 8 unknown", in the fixed
// order of the levels so two shelves read the same way.
func componentCounts(summary jbod.ComponentSummary) string {
	var parts []string
	for _, level := range jbod.HealthLevels {
		if n := summary.Count(level); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, level))
		}
	}
	if len(parts) == 0 {
		return "no elements reported"
	}
	return fmt.Sprintf("%d elements: %s", summary.Total, strings.Join(parts, ", "))
}

// collectionDetail says what the poll got, in pages.
func collectionDetail(status jbod.CollectionStatus) string {
	ok := 0
	for _, page := range status.Pages {
		if page.OK {
			ok++
		}
	}
	detail := fmt.Sprintf("%d/%d pages read", ok, len(status.Pages))
	if generation, has := status.Generation.Get(); has {
		detail += ", generation " + generation
	}
	if status.GenerationChanged {
		detail += " (changed during the pass)"
	}
	return detail
}

// printHealth renders the condition of each shelf.
//
// The three rows are deliberately separate. The hardware row is the
// enclosure's own verdict, the components row is what its elements report,
// and the collection row is how much of that was readable — a partial poll
// is not a fault and must not be printed as one (ROADMAP 5).
func printHealth(out io.Writer, statuses []jbod.EnclosureStatus) error {
	for i, status := range statuses {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%s  health %s\n", statusHeading(status), status.Level())
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SCOPE\tLEVEL\tDETAIL")
		fmt.Fprintf(w, "hardware\t%s\t%s\n", status.Hardware.Level, conditionBits(status.Hardware))
		fmt.Fprintf(w, "components\t%s\t%s\n", status.Summary.Level, componentCounts(status.Summary))
		fmt.Fprintf(w, "collection\t%s\t%s\n", completeness(status.Collection), collectionDetail(status.Collection))
		if err := w.Flush(); err != nil {
			return err
		}
		for _, note := range collectionNotes(status) {
			fmt.Fprintln(out, "note: "+note)
		}
	}
	return nil
}

// completeness is the word the collection row shows.
func completeness(status jbod.CollectionStatus) string {
	if status.Complete {
		return "complete"
	}
	return "partial"
}

// collectionNotes lists what the poll did not get, one line each: the pages
// that failed, the elements the configuration declared but nothing
// reported, and a configuration that changed mid-pass.
func collectionNotes(status jbod.EnclosureStatus) []string {
	var notes []string
	for _, page := range status.Collection.Pages {
		err, failed := page.Err.Get()
		if !failed {
			continue
		}
		word := "did not answer"
		if page.OK {
			// A page that was not required and was not read at all.
			word = "was not read"
		} else if !page.Required {
			word = "did not answer and is not required"
		}
		notes = append(notes, fmt.Sprintf("the %s page %s: %s", page.Name, word, err))
	}
	if n := status.Collection.Missing; n > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d element(s) are declared by the configuration page and were not reported by any status page", n))
	}
	if status.Collection.GenerationChanged {
		notes = append(notes, "the pages of this pass report different generation codes, "+
			"so this report mixes two configurations; run it again")
	}
	if err, ok := status.Hardware.Err.Get(); ok {
		notes = append(notes, "the enclosure status page did not answer, so the hardware verdict is unknown: "+err)
	}
	return notes
}

// number renders a reading. An absent value is a dash and never a zero.
func readingValue(r jbod.Reading) string {
	v, ok := r.Value.Get()
	if !ok {
		return noAttribute
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// threshold renders one limit of a reading.
func threshold(r jbod.Reading, pick func(jbod.Thresholds) jbod.Optional[float64]) string {
	if r.Thresholds == nil {
		return noAttribute
	}
	v, ok := pick(*r.Thresholds).Get()
	if !ok {
		return noAttribute
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// unitSuffix is the short form of a unit, for the component listing.
var unitSuffix = map[string]string{
	jbod.UnitCelsius: "C",
	jbod.UnitRPM:     "rpm",
	jbod.UnitVolts:   "V",
	jbod.UnitAmps:    "A",
}

// readings renders every value of one element for the component table.
func readings(c jbod.Component) string {
	if len(c.Readings) == 0 {
		return noAttribute
	}
	parts := make([]string, 0, len(c.Readings))
	for _, r := range c.Readings {
		// A reading the element did not report is a dash on its own: "- C"
		// reads as a temperature that is somehow missing its digits.
		if !r.Value.Present() {
			parts = append(parts, noAttribute)
			continue
		}
		suffix := unitSuffix[r.Unit]
		if suffix == "" {
			suffix = r.Unit
		}
		parts = append(parts, readingValue(r)+" "+suffix)
	}
	return strings.Join(parts, ", ")
}

// printSensors renders the sensor values of each shelf with the thresholds
// the enclosure declares for them.
func printSensors(out io.Writer, statuses []jbod.EnclosureStatus) error {
	for i, status := range statuses {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, statusHeading(status))
		sensors := status.Sensors()
		if len(sensors) == 0 {
			fmt.Fprintln(out, "no element of this enclosure reports a reading")
			for _, note := range collectionNotes(status) {
				fmt.Fprintln(out, "note: "+note)
			}
			continue
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tTYPE\tREADING\tVALUE\tUNIT\tSTATUS\tHEALTH\tHIGH CRIT\tHIGH WARN\tLOW WARN\tLOW CRIT")
		for _, c := range sensors {
			for _, r := range c.Readings {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					c.Index, name(c), c.Type, r.Kind, readingValue(r), r.Unit,
					c.Status.Or(noAttribute), c.Health,
					threshold(r, func(t jbod.Thresholds) jbod.Optional[float64] { return t.HighCritical }),
					threshold(r, func(t jbod.Thresholds) jbod.Optional[float64] { return t.HighWarning }),
					threshold(r, func(t jbod.Thresholds) jbod.Optional[float64] { return t.LowWarning }),
					threshold(r, func(t jbod.Thresholds) jbod.Optional[float64] { return t.LowCritical }))
			}
		}
		if err := w.Flush(); err != nil {
			return err
		}
		for _, note := range collectionNotes(status) {
			fmt.Fprintln(out, "note: "+note)
		}
	}
	return nil
}

// name is the element descriptor, or a dash when the enclosure publishes
// none. An empty column is harder to read than an explicit absence.
func name(c jbod.Component) string {
	if strings.TrimSpace(c.Name) == "" {
		return noAttribute
	}
	return c.Name
}

// printComponents renders every element of each shelf, including the ones
// the configuration declares and no status page reported.
//
// The SAS address and the device are the two halves of the slot → SAS
// address → disk mapping: the enclosure names the bay, the kernel names the
// disk, and this is where the two meet (ROADMAP 5).
func printComponents(out io.Writer, statuses []jbod.EnclosureStatus) error {
	for i, status := range statuses {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, statusHeading(status))
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTYPE\tNAME\tSTATUS\tHEALTH\tREADINGS\tSLOT\tSAS ADDRESS\tDEVICE\tMAP")
		for _, c := range status.Components {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				c.Index, c.Type, name(c), componentStatus(c), c.Health, readings(c),
				slotCell(c), addresses(c), c.Device.Or(noDevice), c.Map.Or(noDevice))
		}
		if err := w.Flush(); err != nil {
			return err
		}
		for _, note := range collectionNotes(status) {
			fmt.Fprintln(out, "note: "+note)
		}
	}
	return nil
}

// componentStatus renders the condition the enclosure reported. An element
// that only exists in the configuration page is marked as such, because
// "no status" and "declared and never reported" are different findings.
func componentStatus(c jbod.Component) string {
	if c.Declared {
		return "declared only"
	}
	return c.Status.Or(noAttribute)
}

// slotCell renders the bay number of an element that has one.
func slotCell(c jbod.Component) string {
	n, ok := c.SlotNumber.Get()
	if !ok {
		return noAttribute
	}
	return strconv.FormatInt(n, 10)
}

// addresses renders the SAS addresses the enclosure reports for an element.
func addresses(c jbod.Component) string {
	if len(c.SASAddresses) == 0 {
		return noAttribute
	}
	return strings.Join(c.SASAddresses, ",")
}

// The v1.3 connection report (ROADMAP 6).
//
// Two things it must not do. It must not print a zero for a counter that
// was never read: a link nobody could ask about is not a clean link. And it
// must not imply that a phy of the host belongs to the shelf that was
// named — which phy carries which shelf is topology, and the note under the
// table says so rather than the table pretending otherwise.

// linkRateCell renders a link rate. A rate that carries no number keeps its
// spelling, because "Phy disabled" is the answer and 0 Gbit/s is not.
func linkRateCell(rate jbod.LinkRate) string {
	if text, ok := rate.Text.Get(); ok {
		return text
	}
	return noAttribute
}

// counterCell renders one error counter. An absent counter is a dash: zero
// is a claim that the link is clean, and nobody made it.
func counterCell(v jbod.Optional[int64]) string {
	n, ok := v.Get()
	if !ok {
		return noAttribute
	}
	return strconv.FormatInt(n, 10)
}

// phyStateCounts renders the roll-up as "6 up, 2 unknown", in the fixed
// order of the states so two hosts read the same way.
func phyStateCounts(counts map[jbod.PHYState]int, total int) string {
	var parts []string
	for _, state := range jbod.PHYStates {
		if n := counts[state]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, state))
		}
	}
	if len(parts) == 0 {
		return "no phy reported"
	}
	return fmt.Sprintf("%d phys: %s", total, strings.Join(parts, ", "))
}

// phyHeading names one host and what is reached through it.
func phyHeading(report jbod.SASReport) string {
	heading := fmt.Sprintf("Host %d  %s", report.Host, phyStateCounts(report.States, len(report.PHYs)))
	if len(report.Enclosures) > 0 {
		heading += "  (enclosures " + strings.Join(report.Enclosures, ", ") + ")"
	}
	return heading
}

// printPHYs renders the SAS phys of each host, with the error counters the
// hardware keeps for them.
func printPHYs(out io.Writer, reports []jbod.SASReport) error {
	for i, report := range reports {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, phyHeading(report))
		if len(report.PHYs) == 0 {
			fmt.Fprintln(out, "no SAS phy is registered for this host")
		} else if err := printPHYTable(out, report.PHYs); err != nil {
			return err
		}
		if err := printExpanders(out, report.Expanders); err != nil {
			return err
		}
		for _, note := range phyNotes(report) {
			fmt.Fprintln(out, "note: "+note)
		}
	}
	return nil
}

// printPHYTable renders one host's phys.
//
// The four counter columns are the shortened names of the SAS link error
// counters: invalid dwords, running disparity errors, losses of dword
// synchronisation and phy reset problems.
func printPHYTable(out io.Writer, phys []jbod.PHY) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PHY\tPORT\tTYPE\tSAS ADDRESS\tID\tSTATE\tNEGOTIATED\tMAX\tINV DW\tDISP\tSYNC\tRESET")
	for _, phy := range phys {
		id := noAttribute
		if n, ok := phy.Identifier.Get(); ok {
			id = strconv.FormatInt(n, 10)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			phy.Name, phy.Port.Or(noAttribute), phy.DeviceType.Or(noAttribute),
			phy.SASAddress.Or(noAttribute), id, phy.State,
			linkRateCell(phy.Negotiated), linkRateCell(phy.Maximum),
			counterCell(phy.Counters.InvalidDword), counterCell(phy.Counters.RunningDisparityError),
			counterCell(phy.Counters.LossOfDwordSync), counterCell(phy.Counters.PhyResetProblem))
	}
	return w.Flush()
}

// printExpanders renders the expanders of one host as a table, and then the
// phys of each expander that answered over SMP.
//
// The table exists because the common case is that SMP was not asked for or
// is not installed: a shelf with six expanders then produced six one-line
// stanzas with a blank line between them, which is six times the space for
// no more information.
func printExpanders(out io.Writer, expanders []jbod.Expander) error {
	if len(expanders) == 0 {
		return nil
	}
	fmt.Fprintln(out)
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "EXPANDER\tSAS ADDRESS\tIDENTITY\tLEVEL\tPHYS\tSMP DEVICE")
	for _, expander := range expanders {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			expander.Name, expander.SASAddress.Or(noAttribute), expanderIdentity(expander),
			optionalCell(expander.Level), optionalCell(expander.NumPhys),
			expander.SMPDevice.Or(noAttribute))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return printSMPPhys(out, expanders)
}

// printSMPPhys renders the phys of every expander that answered over SMP.
//
// A phy the expander reports as vacant is counted in a note and not given a
// row. It is not an empty bay, which is a place a disk can go and therefore
// belongs in a listing: it is a phy the expander declares in its count and
// says is not there, and its row can only ever be dashes. On a real
// H4060-J that was 192 of 370 rows (ROADMAP 6, hardware run).
func printSMPPhys(out io.Writer, expanders []jbod.Expander) error {
	for _, expander := range expanders {
		if len(expander.Phys) == 0 {
			continue
		}
		fmt.Fprintf(out, "\nExpander %s over SMP (%s)\n", expander.Name, expander.SMPDevice.Or(noAttribute))
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tROUTING\tSTATE\tNEGOTIATED\tATTACHED\tATTACHED ID\tPROTOCOLS\tINV DW\tDISP\tSYNC\tRESET")
		var vacant []int64
		for _, phy := range expander.Phys {
			if phy.State == jbod.PHYStateVacant {
				vacant = append(vacant, phy.Identifier)
				continue
			}
			attachedID := noAttribute
			if n, ok := phy.AttachedPhy.Get(); ok {
				attachedID = strconv.FormatInt(n, 10)
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				phy.Identifier, phy.Routing.Or(noAttribute), phy.State,
				linkRateCell(phy.Negotiated), phy.AttachedAddress.Or(noAttribute), attachedID,
				phy.AttachedProtocols.Or(noAttribute),
				counterCell(phy.Counters.InvalidDword), counterCell(phy.Counters.RunningDisparityError),
				counterCell(phy.Counters.LossOfDwordSync), counterCell(phy.Counters.PhyResetProblem))
		}
		if err := w.Flush(); err != nil {
			return err
		}
		if len(vacant) > 0 {
			fmt.Fprintf(out, "note: %d of %d phys are vacant and not listed (%s): the expander declares "+
				"them in its phy count and reports that they are not there\n",
				len(vacant), len(expander.Phys), numberRanges(vacant))
		}
	}
	return nil
}

// numberRanges renders a sorted list of phy numbers as "0-23, 47".
//
// The numbers are the expander's own, and on a 68-phy expander the vacant
// ones come in runs; printing 37 of them one by one would be the noise the
// note exists to remove.
func numberRanges(numbers []int64) string {
	if len(numbers) == 0 {
		return ""
	}
	var parts []string
	start, previous := numbers[0], numbers[0]
	flush := func() {
		switch {
		case start == previous:
			parts = append(parts, strconv.FormatInt(start, 10))
		default:
			parts = append(parts, strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(previous, 10))
		}
	}
	for _, n := range numbers[1:] {
		if n == previous+1 {
			previous = n
			continue
		}
		flush()
		start, previous = n, n
	}
	flush()
	return strings.Join(parts, ", ")
}

// optionalCell renders an integer the hardware may not report.
func optionalCell(v jbod.Optional[int64]) string {
	n, ok := v.Get()
	if !ok {
		return noAttribute
	}
	return strconv.FormatInt(n, 10)
}

// expanderIdentity renders what the expander says it is.
func expanderIdentity(expander jbod.Expander) string {
	parts := make([]string, 0, 3)
	for _, field := range []jbod.Optional[string]{expander.Vendor, expander.Product, expander.Revision} {
		if v, ok := field.Get(); ok && strings.TrimSpace(v) != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return "identity " + noAttribute
	}
	return strings.Join(parts, " ")
}

// smpTarget says how the expander can be reached over SMP, which is also
// the answer to why it was not.
func smpTarget(expander jbod.Expander) string {
	if device, ok := expander.SMPDevice.Get(); ok {
		return "smp " + device
	}
	return "smp " + noAttribute
}

// phyNotes lists what the report did not get, and the one boundary a table
// of phys cannot show by itself.
func phyNotes(report jbod.SASReport) []string {
	var notes []string
	if err, ok := report.Collection.Err.Get(); ok {
		notes = append(notes, err)
	}
	if n := report.Collection.Unreadable; n > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d phy(s) exposed no SAS transport attribute and are listed without one", n))
	}
	if len(report.Enclosures) > 0 && len(report.PHYs) > 0 {
		// The table is the links of the HBA, not the links of the shelf.
		// Saying so is the difference between a report and a guess: tying
		// a phy to an enclosure is topology (ROADMAP 6).
		notes = append(notes, fmt.Sprintf(
			"these are the phys of host %d, the HBA the listed enclosures are attached through; "+
				"which phy carries which shelf is topology and is not reported here", report.Host))
	}
	if report.Collection.SMP.Requested {
		if err, ok := report.Collection.SMP.Err.Get(); ok {
			notes = append(notes, "SMP: "+err)
		}
		if report.Collection.SMP.Available {
			notes = append(notes, fmt.Sprintf(
				"SMP read the error log of %d phy(s); no counter was cleared", report.Collection.SMP.Phys))
		}
	}
	return notes
}
