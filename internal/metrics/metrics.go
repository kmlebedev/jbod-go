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

// label escapes a label value for the text format.
func label(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(s)
}

// Encode renders the snapshot.
//
// errorTotals carries the cumulative per-collector failure counts, because
// jbod_scrape_errors_total is a counter and must not go backwards between
// scrapes; the exporter keeps them across passes.
func Encode(s jbod.Snapshot, errorTotals map[string]int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP number_of_enclosures Number of enclosures\n# TYPE number_of_enclosures gauge\nnumber_of_enclosures %d\n", len(s.Enclosures))
	b.WriteString("# HELP jbod_slot_temperature Enclosure number, slot position and temperature\n# TYPE jbod_slot_temperature gauge\n")
	// Match the original gauge-vector behavior: the last value wins for
	// duplicate labels.
	temps := map[string]int64{}
	var tempKeys []string
	for _, d := range s.Disks {
		n, ok := d.Temperature.Get()
		if !ok {
			continue
		}
		key := fmt.Sprintf(`slot="%s",enclosure="%s"`, label(d.Slot), label(d.Enclosure))
		if _, seen := temps[key]; !seen {
			tempKeys = append(tempKeys, key)
		}
		temps[key] = n
	}
	for _, key := range tempKeys {
		fmt.Fprintf(&b, "jbod_slot_temperature{%s} %d\n", key, temps[key])
	}
	b.WriteString("# HELP jbod_fan_rpm The RPM speed of FAN components, device and slot\n# TYPE jbod_fan_rpm gauge\n")
	speeds := map[string]int64{}
	var fanKeys []string
	for _, f := range s.Fans {
		key := fmt.Sprintf(`device="%s",slot="%s"`, label(f.Description), label(f.Index))
		if _, seen := speeds[key]; !seen {
			fanKeys = append(fanKeys, key)
		}
		speeds[key] = f.Speed
	}
	for _, key := range fanKeys {
		fmt.Fprintf(&b, "jbod_fan_rpm{%s} %d\n", key, speeds[key])
	}
	// Health of the scrape itself: a partial collection is reported through
	// these series instead of an HTTP error (B5).
	up := 0
	if s.Up {
		up = 1
	}
	fmt.Fprintf(&b, "# HELP jbod_up Whether the last collection completed\n# TYPE jbod_up gauge\njbod_up %d\n", up)
	b.WriteString("# HELP jbod_scrape_duration_seconds Duration of the last collection\n# TYPE jbod_scrape_duration_seconds gauge\n")
	fmt.Fprintf(&b, "jbod_scrape_duration_seconds %s\n", strconv.FormatFloat(s.Duration.Seconds(), 'f', 3, 64))
	b.WriteString("# HELP jbod_scrape_errors_total Failed collection operations per collector\n# TYPE jbod_scrape_errors_total counter\n")
	for _, name := range collectors(errorTotals) {
		fmt.Fprintf(&b, "jbod_scrape_errors_total{collector=\"%s\"} %d\n", label(name), errorTotals[name])
	}
	return b.String()
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
