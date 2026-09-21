// SPDX-License-Identifier: BSD-2-Clause

package metrics

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// The v1.2 series: enclosure health, the components behind it, the sensor
// readings with their thresholds, the slot → SAS address → disk mapping,
// and how complete the collection was (ROADMAP 5).
//
// Two rules decide what is published here:
//
//   - A reading the hardware did not report gets no series at all. A zero
//     temperature and a zero RPM are values an operator can act on, and
//     publishing one for a sensor that said nothing is how a dead sensor
//     becomes a cold disk.
//
//   - Health is a label, not a number. A numeric severity would have to put
//     "unknown" somewhere on the scale, and every place is wrong: next to
//     ok it hides a shelf nobody could read, and next to critical it pages
//     somebody at three in the morning because a threshold page is not
//     implemented.

// encodeEnclosureHealth publishes the condition of each shelf.
//
// Two sources are published separately because they answer different
// questions: "hardware" is the enclosure's own verdict from its status
// page, and "components" is what its elements report. A shelf that says
// CRIT while every element reads OK is a real situation, and one series
// could not show it.
func encodeEnclosureHealth(b *strings.Builder, s jbod.Snapshot) {
	if len(s.Status) == 0 {
		return
	}
	b.WriteString("# HELP jbod_enclosure_health Enclosure condition; 1 marks the level the source reports\n")
	b.WriteString("# TYPE jbod_enclosure_health gauge\n")
	for _, status := range s.Status {
		for _, source := range []struct {
			name  string
			level jbod.HealthLevel
		}{
			{"hardware", status.Hardware.Level},
			{"components", status.Summary.Level},
		} {
			for _, level := range jbod.HealthLevels {
				// Every level gets a series, so a shelf that recovers
				// publishes a zero instead of leaving a stale critical
				// series behind.
				value := 0
				if level == source.level {
					value = 1
				}
				fmt.Fprintf(b, "jbod_enclosure_health{enclosure=\"%s\",enclosure_id=\"%s\",source=\"%s\",level=\"%s\"} %d\n",
					label(status.Enclosure), label(status.Address), source.name, level, value)
			}
		}
	}
}

// encodeComponents publishes the element roll-up and one info series per
// element.
//
// The roll-up is what an alert is built on — "two power supplies, one of
// them critical" — and the per-element series is what a dashboard drills
// into. Counting is done per type because a shelf with a failed fan and a
// shelf with a failed power supply need different people.
func encodeComponents(b *strings.Builder, s jbod.Snapshot) {
	if len(s.Status) == 0 {
		return
	}
	b.WriteString("# HELP jbod_enclosure_components Declared elements per enclosure, by type and condition\n")
	b.WriteString("# TYPE jbod_enclosure_components gauge\n")
	type key struct {
		enclosure, id, kind string
		level               jbod.HealthLevel
	}
	counts := map[key]int{}
	var order []key
	for _, status := range s.Status {
		for _, c := range status.Components {
			k := key{status.Enclosure, status.Address, c.Type, c.Health}
			if _, seen := counts[k]; !seen {
				order = append(order, k)
			}
			counts[k]++
		}
	}
	slices.SortStableFunc(order, func(a, b key) int {
		if a.enclosure != b.enclosure {
			return strings.Compare(a.enclosure, b.enclosure)
		}
		if a.kind != b.kind {
			return strings.Compare(a.kind, b.kind)
		}
		return strings.Compare(string(a.level), string(b.level))
	})
	for _, k := range order {
		fmt.Fprintf(b, "jbod_enclosure_components{enclosure=\"%s\",enclosure_id=\"%s\",type=\"%s\",health=\"%s\"} %d\n",
			label(k.enclosure), label(k.id), label(k.kind), label(string(k.level)), counts[k])
	}
	b.WriteString("# HELP jbod_component_info Condition of one element; status is the enclosure's own spelling\n")
	b.WriteString("# TYPE jbod_component_info gauge\n")
	for _, status := range s.Status {
		for _, c := range status.Components {
			fmt.Fprintf(b, "jbod_component_info{enclosure=\"%s\",enclosure_id=\"%s\",component=\"%s\",component_id=\"%s\",type=\"%s\",status=\"%s\",health=\"%s\"} 1\n",
				label(status.Enclosure), label(status.Address), label(c.Name), label(c.Index),
				label(c.Type), label(c.Status.Or("")), label(string(c.Health)))
		}
	}
}

// sensorMetric is the series name and the threshold series name of one
// reading kind.
var sensorMetrics = []struct {
	kind      string
	unit      string
	value     string
	threshold string
	help      string
}{
	{jbod.ReadingTemperature, jbod.UnitCelsius, "jbod_sensor_temperature_celsius", "jbod_sensor_temperature_threshold_celsius", "Temperature reported by an enclosure element"},
	{jbod.ReadingVoltage, jbod.UnitVolts, "jbod_sensor_voltage_volts", "jbod_sensor_voltage_threshold_volts", "Voltage reported by an enclosure element"},
	{jbod.ReadingCurrent, jbod.UnitAmps, "jbod_sensor_current_amps", "jbod_sensor_current_threshold_amps", "Current reported by an enclosure element"},
}

// thresholdSeries are the four limits, in the order they are published.
var thresholdSeries = []struct {
	name string
	pick func(jbod.Thresholds) jbod.Optional[float64]
}{
	{"high_critical", func(t jbod.Thresholds) jbod.Optional[float64] { return t.HighCritical }},
	{"high_warning", func(t jbod.Thresholds) jbod.Optional[float64] { return t.HighWarning }},
	{"low_warning", func(t jbod.Thresholds) jbod.Optional[float64] { return t.LowWarning }},
	{"low_critical", func(t jbod.Thresholds) jbod.Optional[float64] { return t.LowCritical }},
}

// encodeSensors publishes the enclosure's own sensors and the thresholds it
// declares for them.
//
// The thresholds are published as series of their own rather than folded
// into an alerting rule, because they are the enclosure's numbers: a rule
// that hard-codes 60 °C is wrong on the next shelf, and one that compares
// against these is not.
func encodeSensors(b *strings.Builder, s jbod.Snapshot) {
	if len(s.Status) == 0 {
		return
	}
	for _, metric := range sensorMetrics {
		var values, limits strings.Builder
		for _, status := range s.Status {
			for _, c := range status.Components {
				for _, r := range c.Readings {
					if r.Kind != metric.kind {
						continue
					}
					labels := fmt.Sprintf(`enclosure="%s",enclosure_id="%s",component="%s",component_id="%s",type="%s"`,
						label(status.Enclosure), label(status.Address), label(c.Name), label(c.Index), label(c.Type))
					if v, ok := r.Value.Get(); ok {
						fmt.Fprintf(&values, "%s{%s} %s\n", metric.value, labels, float(v))
					}
					if r.Thresholds == nil {
						continue
					}
					for _, limit := range thresholdSeries {
						v, ok := limit.pick(*r.Thresholds).Get()
						if !ok {
							continue
						}
						fmt.Fprintf(&limits, "%s{%s,threshold=\"%s\"} %s\n", metric.threshold, labels, limit.name, float(v))
					}
				}
			}
		}
		if values.Len() > 0 {
			fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s", metric.value, metric.help, metric.value, values.String())
		}
		if limits.Len() > 0 {
			fmt.Fprintf(b, "# HELP %s Threshold the enclosure declares for %s\n# TYPE %s gauge\n%s",
				metric.threshold, metric.kind, metric.threshold, limits.String())
		}
	}
}

// encodeMapping publishes the slot → SAS address → disk mapping as an info
// series, which is what lets a dashboard label a disk by the bay it sits in
// (ROADMAP 5).
//
// Only bays with an address are published: an element with no address maps
// to nothing, and an empty label would join to every other empty one.
func encodeMapping(b *strings.Builder, s jbod.Snapshot) {
	var body strings.Builder
	for _, status := range s.Status {
		for _, c := range status.Components {
			if !c.IsBay() || len(c.SASAddresses) == 0 {
				continue
			}
			slot := ""
			if n, ok := c.SlotNumber.Get(); ok {
				slot = strconv.FormatInt(n, 10)
			}
			for _, address := range c.SASAddresses {
				fmt.Fprintf(&body, "jbod_slot_sas_address_info{enclosure=\"%s\",enclosure_id=\"%s\",slot=\"%s\",component_id=\"%s\",sas_address=\"%s\",device=\"%s\",block_device=\"%s\"} 1\n",
					label(status.Enclosure), label(status.Address), label(slot), label(c.Index),
					label(address), label(c.Device.Or("")), label(c.Map.Or("")))
			}
		}
	}
	if body.Len() == 0 {
		return
	}
	b.WriteString("# HELP jbod_slot_sas_address_info Bay, SAS address and the disk the kernel sees in it\n")
	b.WriteString("# TYPE jbod_slot_sas_address_info gauge\n")
	b.WriteString(body.String())
}

// encodeCollection publishes how complete the collection was, per shelf and
// per page.
//
// This is the other half of the health report: jbod_enclosure_health says
// what the shelf reports, and these say how much of it was readable. An
// alert on a critical enclosure and an alert on a shelf that stopped
// answering are different alerts, and they need different series
// (ROADMAP 5).
func encodeCollection(b *strings.Builder, s jbod.Snapshot) {
	if !s.ReadAt.IsZero() {
		b.WriteString("# HELP jbod_snapshot_timestamp_seconds When the collection behind this scrape started\n")
		b.WriteString("# TYPE jbod_snapshot_timestamp_seconds gauge\n")
		fmt.Fprintf(b, "jbod_snapshot_timestamp_seconds %s\n",
			strconv.FormatFloat(float64(s.ReadAt.UnixNano())/1e9, 'f', 3, 64))
	}
	if len(s.Status) == 0 {
		return
	}
	b.WriteString("# HELP jbod_collection_complete Whether every required SES page answered and the pages agreed\n")
	b.WriteString("# TYPE jbod_collection_complete gauge\n")
	for _, status := range s.Status {
		fmt.Fprintf(b, "jbod_collection_complete{enclosure=\"%s\",enclosure_id=\"%s\"} %d\n",
			label(status.Enclosure), label(status.Address), boolean(status.Collection.Complete))
	}
	b.WriteString("# HELP jbod_ses_page_read Whether one SES diagnostic page answered\n")
	b.WriteString("# TYPE jbod_ses_page_read gauge\n")
	for _, status := range s.Status {
		for _, page := range status.Collection.Pages {
			fmt.Fprintf(b, "jbod_ses_page_read{enclosure=\"%s\",enclosure_id=\"%s\",page=\"%s\",required=\"%t\"} %d\n",
				label(status.Enclosure), label(status.Address), label(page.Name), page.Required, boolean(page.OK))
		}
	}
	b.WriteString("# HELP jbod_enclosure_generation_changed Whether the SES pages of one pass described different configurations\n")
	b.WriteString("# TYPE jbod_enclosure_generation_changed gauge\n")
	for _, status := range s.Status {
		fmt.Fprintf(b, "jbod_enclosure_generation_changed{enclosure=\"%s\",enclosure_id=\"%s\"} %d\n",
			label(status.Enclosure), label(status.Address), boolean(status.Collection.GenerationChanged))
	}
	b.WriteString("# HELP jbod_enclosure_components_missing Elements the configuration page declares that no status page reported\n")
	b.WriteString("# TYPE jbod_enclosure_components_missing gauge\n")
	for _, status := range s.Status {
		fmt.Fprintf(b, "jbod_enclosure_components_missing{enclosure=\"%s\",enclosure_id=\"%s\"} %d\n",
			label(status.Enclosure), label(status.Address), status.Collection.Missing)
	}
}

// float renders a reading without an exponent and without trailing zeros,
// so 12.01 volts stays 12.01 and 35 degrees stays 35.
func float(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// boolean renders a flag as the 0 or 1 the text format expects.
func boolean(v bool) int {
	if v {
		return 1
	}
	return 0
}
