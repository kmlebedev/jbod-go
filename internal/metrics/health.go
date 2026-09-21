// SPDX-License-Identifier: BSD-2-Clause

package metrics

import (
	"slices"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

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

// componentLabels is the address of one element, repeated by several series.
var componentLabels = []string{"enclosure", "enclosure_id", "component", "component_id", "type"}

var (
	descEnclosureHealth = prometheus.NewDesc(
		"jbod_enclosure_health",
		"Enclosure condition; 1 marks the level the source reports",
		[]string{"enclosure", "enclosure_id", "source", "level"}, nil)
	descComponents = prometheus.NewDesc(
		"jbod_enclosure_components",
		"Declared elements per enclosure, by type and condition",
		[]string{"enclosure", "enclosure_id", "type", "health"}, nil)
	descComponentInfo = prometheus.NewDesc(
		"jbod_component_info",
		"Condition of one element; status is the enclosure's own spelling",
		[]string{"enclosure", "enclosure_id", "component", "component_id", "type", "status", "health"}, nil)
	descMapping = prometheus.NewDesc(
		"jbod_slot_sas_address_info",
		"Bay, SAS address and the disk the kernel sees in it",
		[]string{"enclosure", "enclosure_id", "slot", "component_id", "sas_address", "device", "block_device"}, nil)
	descSnapshotTimestamp = prometheus.NewDesc(
		"jbod_snapshot_timestamp_seconds",
		"When the collection behind this scrape started",
		nil, nil)
	descCollectionComplete = prometheus.NewDesc(
		"jbod_collection_complete",
		"Whether every required SES page answered and the pages agreed",
		[]string{"enclosure", "enclosure_id"}, nil)
	descPageRead = prometheus.NewDesc(
		"jbod_ses_page_read",
		"Whether one SES diagnostic page answered",
		[]string{"enclosure", "enclosure_id", "page", "required"}, nil)
	descGenerationChanged = prometheus.NewDesc(
		"jbod_enclosure_generation_changed",
		"Whether the SES pages of one pass described different configurations",
		[]string{"enclosure", "enclosure_id"}, nil)
	descComponentsMissing = prometheus.NewDesc(
		"jbod_enclosure_components_missing",
		"Elements the configuration page declares that no status page reported",
		[]string{"enclosure", "enclosure_id"}, nil)
)

// sensorSeries is one reading kind with the two series it feeds.
type sensorSeries struct {
	kind      string
	value     *prometheus.Desc
	threshold *prometheus.Desc
}

// newSensor builds the descriptors of one reading kind next to the kind
// itself, so adding a kind is one entry rather than four edits.
func newSensor(kind, value, threshold, help string) sensorSeries {
	return sensorSeries{
		kind:  kind,
		value: prometheus.NewDesc(value, help, componentLabels, nil),
		threshold: prometheus.NewDesc(threshold,
			"Threshold the enclosure declares for "+kind,
			append(slices.Clone(componentLabels), "threshold"), nil),
	}
}

// sensorMetrics is every reading kind the enclosure can report.
var sensorMetrics = []sensorSeries{
	newSensor(jbod.ReadingTemperature,
		"jbod_sensor_temperature_celsius", "jbod_sensor_temperature_threshold_celsius",
		"Temperature reported by an enclosure element"),
	newSensor(jbod.ReadingVoltage,
		"jbod_sensor_voltage_volts", "jbod_sensor_voltage_threshold_volts",
		"Voltage reported by an enclosure element"),
	newSensor(jbod.ReadingCurrent,
		"jbod_sensor_current_amps", "jbod_sensor_current_threshold_amps",
		"Current reported by an enclosure element"),
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

// healthDescriptors is what this file can publish, for Describe.
var healthDescriptors = func() []*prometheus.Desc {
	descs := []*prometheus.Desc{
		descEnclosureHealth, descComponents, descComponentInfo, descMapping,
		descSnapshotTimestamp, descCollectionComplete, descPageRead,
		descGenerationChanged, descComponentsMissing,
	}
	for _, metric := range sensorMetrics {
		descs = append(descs, metric.value, metric.threshold)
	}
	return descs
}()

// collectEnclosureHealth publishes the condition of each shelf.
//
// Two sources are published separately because they answer different
// questions: "hardware" is the enclosure's own verdict from its status
// page, and "components" is what its elements report. A shelf that says
// CRIT while every element reads OK is a real situation, and one series
// could not show it.
func collectEnclosureHealth(s *sink, snapshot jbod.Snapshot) {
	for _, status := range snapshot.Status {
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
				s.gauge(descEnclosureHealth, boolean(level == source.level),
					status.Enclosure, status.Address, source.name, string(level))
			}
		}
	}
}

// collectComponents publishes the element roll-up and one info series per
// element.
//
// The roll-up is what an alert is built on — "two power supplies, one of
// them critical" — and the per-element series is what a dashboard drills
// into. Counting is done per type because a shelf with a failed fan and a
// shelf with a failed power supply need different people.
func collectComponents(s *sink, snapshot jbod.Snapshot) {
	type rollup struct {
		enclosure, id, kind string
		level               jbod.HealthLevel
	}
	counts := map[rollup]int{}
	var order []rollup
	for _, status := range snapshot.Status {
		for _, c := range status.Components {
			k := rollup{status.Enclosure, status.Address, c.Type, c.Health}
			if _, seen := counts[k]; !seen {
				order = append(order, k)
			}
			counts[k]++
		}
	}
	slices.SortStableFunc(order, func(a, b rollup) int {
		if a.enclosure != b.enclosure {
			return strings.Compare(a.enclosure, b.enclosure)
		}
		if a.kind != b.kind {
			return strings.Compare(a.kind, b.kind)
		}
		return strings.Compare(string(a.level), string(b.level))
	})
	for _, k := range order {
		s.gauge(descComponents, float64(counts[k]), k.enclosure, k.id, k.kind, string(k.level))
	}
	for _, status := range snapshot.Status {
		for _, c := range status.Components {
			s.gauge(descComponentInfo, 1,
				status.Enclosure, status.Address, c.Name, c.Index,
				c.Type, c.Status.Or(""), string(c.Health))
		}
	}
}

// collectSensors publishes the enclosure's own sensors and the thresholds it
// declares for them.
//
// The thresholds are published as series of their own rather than folded
// into an alerting rule, because they are the enclosure's numbers: a rule
// that hard-codes 60 °C is wrong on the next shelf, and one that compares
// against these is not.
func collectSensors(s *sink, snapshot jbod.Snapshot) {
	for _, metric := range sensorMetrics {
		for _, status := range snapshot.Status {
			for _, c := range status.Components {
				for _, r := range c.Readings {
					if r.Kind != metric.kind {
						continue
					}
					labels := []string{status.Enclosure, status.Address, c.Name, c.Index, c.Type}
					if v, ok := r.Value.Get(); ok {
						s.gauge(metric.value, v, labels...)
					}
					if r.Thresholds == nil {
						continue
					}
					for _, limit := range thresholdSeries {
						v, ok := limit.pick(*r.Thresholds).Get()
						if !ok {
							continue
						}
						s.gauge(metric.threshold, v, append(slices.Clone(labels), limit.name)...)
					}
				}
			}
		}
	}
}

// collectMapping publishes the slot → SAS address → disk mapping as an info
// series, which is what lets a dashboard label a disk by the bay it sits in
// (ROADMAP 5).
//
// Only bays with an address are published: an element with no address maps
// to nothing, and an empty label would join to every other empty one.
func collectMapping(s *sink, snapshot jbod.Snapshot) {
	for _, status := range snapshot.Status {
		for _, c := range status.Components {
			if !c.IsBay() || len(c.SASAddresses) == 0 {
				continue
			}
			slot := ""
			if n, ok := c.SlotNumber.Get(); ok {
				slot = number(n)
			}
			for _, address := range c.SASAddresses {
				s.gauge(descMapping, 1,
					status.Enclosure, status.Address, slot, c.Index,
					address, c.Device.Or(""), c.Map.Or(""))
			}
		}
	}
}

// collectCollection publishes how complete the collection was, per shelf and
// per page.
//
// This is the other half of the health report: jbod_enclosure_health says
// what the shelf reports, and these say how much of it was readable. An
// alert on a critical enclosure and an alert on a shelf that stopped
// answering are different alerts, and they need different series
// (ROADMAP 5).
func collectCollection(s *sink, snapshot jbod.Snapshot) {
	if !snapshot.ReadAt.IsZero() {
		s.gauge(descSnapshotTimestamp, float64(snapshot.ReadAt.UnixNano())/1e9)
	}
	for _, status := range snapshot.Status {
		s.gauge(descCollectionComplete, boolean(status.Collection.Complete), status.Enclosure, status.Address)
		for _, page := range status.Collection.Pages {
			s.gauge(descPageRead, boolean(page.OK),
				status.Enclosure, status.Address, page.Name, strconv.FormatBool(page.Required))
		}
		s.gauge(descGenerationChanged, boolean(status.Collection.GenerationChanged), status.Enclosure, status.Address)
		s.gauge(descComponentsMissing, float64(status.Collection.Missing), status.Enclosure, status.Address)
	}
}
