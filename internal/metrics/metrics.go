// SPDX-License-Identifier: BSD-2-Clause

// Package metrics encodes a collection snapshot as Prometheus text format
// 0.0.4. It does no I/O: what it renders is decided by the snapshot it is
// given, which keeps the format testable without a shelf.
package metrics

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// Options controls what Encode renders.
type Options struct {
	// Deprecated keeps the pre-1.1 series in the output. It defaults to
	// off in the zero value, and the exporter turns it on, because the
	// migration period is the default and dropping a series under a
	// running dashboard is not (ROADMAP 4).
	Deprecated bool
}

// label escapes a label value for the text format.
func label(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(s)
}

// Encode renders the snapshot.
//
// errorTotals carries the cumulative per-collector failure counts, because
// jbod_scrape_errors_total is a counter and must not go backwards between
// scrapes; the exporter keeps them across passes.
func Encode(s jbod.Snapshot, errorTotals map[string]int, opts Options) string {
	var b strings.Builder
	encodeEnclosures(&b, s)
	encodeSlots(&b, s)
	encodeTemperatures(&b, s)
	encodeFans(&b, s, opts)
	encodeHealth(&b, s, errorTotals)
	return b.String()
}

// encodeEnclosures publishes the identity of each shelf as an info metric.
//
// This is the join target for every other series: jbod_slot_temperature and
// the rest are labelled with the SCSI address, which is a location and gets
// reassigned, so a dashboard that needs a stable identity joins on
// enclosure and reads enclosure_id here. id_source says whether that
// identity is really stable or just the address again (ROADMAP 3).
func encodeEnclosures(b *strings.Builder, s jbod.Snapshot) {
	fmt.Fprintf(b, "# HELP number_of_enclosures Number of enclosures\n# TYPE number_of_enclosures gauge\nnumber_of_enclosures %d\n", len(s.Enclosures))
	b.WriteString("# HELP jbod_enclosure_info Identity of each enclosure; id_source is logical, serial or address\n# TYPE jbod_enclosure_info gauge\n")
	for _, e := range s.Enclosures {
		id, _ := e.Ref()
		fmt.Fprintf(b, "jbod_enclosure_info{enclosure=\"%s\",enclosure_id=\"%s\",id_source=\"%s\",vendor=\"%s\",model=\"%s\",revision=\"%s\",serial=\"%s\"} 1\n",
			label(e.Slot), label(id), label(e.IDSource()),
			label(e.Vendor.Or("")), label(e.Model.Or("")), label(e.Revision.Or("")), label(e.Serial.Or("")))
	}
}

// encodeSlots counts the bays per state.
//
// The three states are reported separately so a shelf that lost a drive
// shows up as one fewer occupied and one more empty, rather than as a slot
// that silently stopped existing. A slot that could not be read is its own
// state and never folded into empty (ROADMAP 4).
func encodeSlots(b *strings.Builder, s jbod.Snapshot) {
	b.WriteString("# HELP jbod_enclosure_slots Slots per enclosure by occupancy\n# TYPE jbod_enclosure_slots gauge\n")
	type key struct {
		enclosure, id, occupancy string
	}
	counts := map[key]int{}
	var order []key
	ids := enclosureIDs(s)
	// Every enclosure gets a series for every state, so a count going to
	// zero is visible instead of the series disappearing.
	for _, e := range s.Enclosures {
		for _, occupancy := range []jbod.Occupancy{jbod.OccupancyOccupied, jbod.OccupancyEmpty, jbod.OccupancyUnavailable} {
			k := key{e.Slot, ids[e.Slot], string(occupancy)}
			if _, seen := counts[k]; !seen {
				counts[k] = 0
				order = append(order, k)
			}
		}
	}
	for _, slot := range s.Slots {
		k := key{slot.Enclosure, ids[slot.Enclosure], string(slot.Occupancy)}
		if _, seen := counts[k]; !seen {
			order = append(order, k)
		}
		counts[k]++
	}
	for _, k := range order {
		fmt.Fprintf(b, "jbod_enclosure_slots{enclosure=\"%s\",enclosure_id=\"%s\",occupancy=\"%s\"} %d\n",
			label(k.enclosure), label(k.id), label(k.occupancy), counts[k])
	}
}

func encodeTemperatures(b *strings.Builder, s jbod.Snapshot) {
	b.WriteString("# HELP jbod_slot_temperature Enclosure number, slot position and temperature\n# TYPE jbod_slot_temperature gauge\n")
	// Match the original gauge-vector behavior: the last value wins for
	// duplicate labels.
	temps := map[string]int64{}
	var keys []string
	for _, d := range s.Disks {
		n, ok := d.Temperature.Get()
		if !ok {
			continue
		}
		key := fmt.Sprintf(`slot="%s",enclosure="%s"`, label(d.Slot), label(d.Enclosure))
		if _, seen := temps[key]; !seen {
			keys = append(keys, key)
		}
		temps[key] = n
	}
	for _, key := range keys {
		fmt.Fprintf(b, "jbod_slot_temperature{%s} %d\n", key, temps[key])
	}
}

// encodeFans publishes the corrected fan series, and optionally the old one.
//
// jbod_fan_rpm labels a fan with its description and its SES index only, so
// "Fan A" at index 2,0 means the same thing on every shelf in the rack and
// the second enclosure overwrites the first. There is no way to fix that in
// place without changing what the existing series means, so the corrected
// metric is a new name carrying the enclosure as well, and the old one stays
// until 2.0 (ROADMAP 4).
func encodeFans(b *strings.Builder, s jbod.Snapshot, opts Options) {
	ids := enclosureIDs(s)
	b.WriteString("# HELP jbod_fan_speed_rpm Speed of a cooling element, addressed by enclosure and component\n# TYPE jbod_fan_speed_rpm gauge\n")
	speeds := map[string]int64{}
	var keys []string
	for _, f := range s.Fans {
		id := ids[f.Slot]
		if id == "" {
			id = f.Serial.Or(f.Slot)
		}
		key := fmt.Sprintf(`enclosure="%s",enclosure_id="%s",component="%s",component_id="%s"`,
			label(f.Slot), label(id), label(f.Description), label(f.Index))
		if _, seen := speeds[key]; !seen {
			keys = append(keys, key)
		}
		speeds[key] = f.Speed
	}
	for _, key := range keys {
		fmt.Fprintf(b, "jbod_fan_speed_rpm{%s} %d\n", key, speeds[key])
	}
	if !opts.Deprecated {
		return
	}
	b.WriteString("# HELP jbod_fan_rpm DEPRECATED, replaced by jbod_fan_speed_rpm: the labels omit the enclosure, so identical fans of two shelves overwrite each other. Removal is planned for 2.0.\n# TYPE jbod_fan_rpm gauge\n")
	old := map[string]int64{}
	keys = keys[:0]
	for _, f := range s.Fans {
		key := fmt.Sprintf(`device="%s",slot="%s"`, label(f.Description), label(f.Index))
		if _, seen := old[key]; !seen {
			keys = append(keys, key)
		}
		old[key] = f.Speed
	}
	for _, key := range keys {
		fmt.Fprintf(b, "jbod_fan_rpm{%s} %d\n", key, old[key])
	}
}

func encodeHealth(b *strings.Builder, s jbod.Snapshot, errorTotals map[string]int) {
	// Health of the scrape itself: a partial collection is reported through
	// these series instead of an HTTP error (B5).
	up := 0
	if s.Up {
		up = 1
	}
	fmt.Fprintf(b, "# HELP jbod_up Whether the last collection completed\n# TYPE jbod_up gauge\njbod_up %d\n", up)
	b.WriteString("# HELP jbod_scrape_duration_seconds Duration of the last collection\n# TYPE jbod_scrape_duration_seconds gauge\n")
	fmt.Fprintf(b, "jbod_scrape_duration_seconds %s\n", strconv.FormatFloat(s.Duration.Seconds(), 'f', 3, 64))
	b.WriteString("# HELP jbod_scrape_errors_total Failed collection operations per collector\n# TYPE jbod_scrape_errors_total counter\n")
	for _, name := range collectors(errorTotals) {
		fmt.Fprintf(b, "jbod_scrape_errors_total{collector=\"%s\"} %d\n", label(name), errorTotals[name])
	}
}

// enclosureIDs maps each shelf's SCSI address to the identifier other
// series should be joined on.
func enclosureIDs(s jbod.Snapshot) map[string]string {
	ids := make(map[string]string, len(s.Enclosures))
	for _, e := range s.Enclosures {
		id, _ := e.Ref()
		ids[e.Slot] = id
	}
	return ids
}

// collectors lists the known collectors first, then anything else the totals
// mention, so a new collector cannot silently drop out of the output.
func collectors(errorTotals map[string]int) []string {
	names := slices.Clone(jbod.Collectors)
	var extra []string
	for name := range errorTotals {
		if !slices.Contains(names, name) {
			extra = append(extra, name)
		}
	}
	slices.Sort(extra)
	return append(names, extra...)
}
