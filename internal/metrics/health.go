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

// componentLabels is the address of one element of a shelf, repeated by
// several series. It has no enclosure: an element is published once per
// shelf, whichever module's answer it is (see chassis.go).
var componentLabels = []string{"enclosure_id", "component", "component_id", "type"}

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
		"Condition of one element of a shelf; status is the enclosure's own spelling",
		[]string{"enclosure_id", "component", "component_id", "type", "status", "health"}, nil)
	descComponentFlag = prometheus.NewDesc(
		"jbod_component_flag",
		"A status bit an element has set, under a stable name; the series exists only while "+
			"the bit is set, so 1 is its only value",
		append(slices.Clone(componentLabels), "flag"), nil)
	descComponentFlags = prometheus.NewDesc(
		"jbod_enclosure_component_flags",
		"Elements of a type with a status bit set, for every bit the shelf reports for "+
			"that type; 0 when none has it",
		[]string{"enclosure_id", "type", "flag"}, nil)
	descMapping = prometheus.NewDesc(
		"jbod_slot_sas_address_info",
		"Bay, SAS address and the disk the kernel sees in it",
		[]string{"enclosure_id", "slot", "component_id", "sas_address", "device", "block_device"}, nil)
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
	kind  string
	value *prometheus.Desc
	// threshold publishes the limits of this kind, and thresholdUnit is the
	// unit they have to be in to be published under it. A limit in any
	// other unit gets no series: publishing a percentage under a name that
	// ends in _volts would be a number with the wrong meaning.
	threshold     *prometheus.Desc
	thresholdUnit string
}

// newSensor builds the descriptors of one reading kind next to the kind
// itself, so adding a kind is one entry rather than four edits.
func newSensor(kind, value, help, threshold, thresholdUnit, thresholdHelp string) sensorSeries {
	return sensorSeries{
		kind:  kind,
		value: prometheus.NewDesc(value, help, componentLabels, nil),
		threshold: prometheus.NewDesc(threshold, thresholdHelp,
			append(slices.Clone(componentLabels), "threshold"), nil),
		thresholdUnit: thresholdUnit,
	}
}

// sensorMetrics is every reading kind the enclosure can report.
//
// The Threshold In page states temperature limits in degrees, and voltage
// and current limits as a percentage of the sensor's nominal value, which
// no page reports. The names say which: comparing a voltage with its limit
// takes the nominal, and a series that pretended to be in volts would hide
// that.
var sensorMetrics = []sensorSeries{
	newSensor(jbod.ReadingTemperature,
		"jbod_sensor_temperature_celsius", "Temperature reported by an enclosure element",
		"jbod_sensor_temperature_threshold_celsius", jbod.UnitCelsius,
		"Temperature limit the enclosure declares for an element"),
	newSensor(jbod.ReadingVoltage,
		"jbod_sensor_voltage_volts", "Voltage reported by an enclosure element",
		"jbod_sensor_voltage_threshold_percent", jbod.UnitPercentOfNominal,
		"Voltage limit the enclosure declares for an element, in percent of the nominal voltage: "+
			"high limits above it, low limits below it"),
	newSensor(jbod.ReadingCurrent,
		"jbod_sensor_current_amps", "Current reported by an enclosure element",
		"jbod_sensor_current_threshold_percent", jbod.UnitPercentOfNominal,
		"Current limit the enclosure declares for an element, in percent above the nominal current"),
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
		descEnclosureHealth, descComponents, descComponentInfo, descComponentFlag, descComponentFlags,
		descMapping,
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
	// The bits behind the status: a power supply that is OK with "AC fail"
	// set is on its last input, and a bay that is OK with "Predicted
	// failure" set holds a disk that said it is dying.
	//
	// They are published in two shapes, because nearly all of them are 0
	// nearly all the time: on a WD H4060-J 25 of the 1961 bits each module
	// reports are set, and every one of those is a normal state. Publishing
	// each bit as 0 or 1 was 3922 series per host that said nothing. So:
	//
	//   - per element, only the bits that are set. A bit that clears ends
	//     its series; that the element was read at all is
	//     jbod_component_info, so a missing flag next to a present element
	//     means the bit is clear, not that nobody looked.
	//   - per enclosure, type and bit, the count of elements that have it,
	//     zeros included. That series is always there, so the moment a bit
	//     sets or clears is a step with history — "predicted failure went
	//     from 0 to 1", "one connector fewer is mated" — which is what an
	//     alert is written on. The per-element series then says which.
	type flagKey struct{ id, kind, flag string }
	flagCounts := map[flagKey]int{}
	var flagOrder []flagKey
	for _, shelf := range shelves(snapshot) {
		for _, c := range shelf.components {
			s.gauge(descComponentInfo, 1,
				shelf.id, c.Name, c.Index, c.Type, c.Status.Or(""), string(c.Health))
			for _, flag := range c.StatusFlags() {
				k := flagKey{shelf.id, c.Type, flag.Name}
				if _, seen := flagCounts[k]; !seen {
					flagOrder = append(flagOrder, k)
					flagCounts[k] = 0
				}
				if !flag.Set {
					continue
				}
				flagCounts[k]++
				s.gauge(descComponentFlag, 1, shelf.id, c.Name, c.Index, c.Type, flag.Name)
			}
		}
	}
	for _, k := range flagOrder {
		s.gauge(descComponentFlags, float64(flagCounts[k]), k.id, k.kind, k.flag)
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
	merged := shelves(snapshot)
	for _, metric := range sensorMetrics {
		for _, shelf := range merged {
			for _, c := range shelf.components {
				for _, r := range c.Readings {
					if r.Kind != metric.kind {
						continue
					}
					labels := []string{shelf.id, c.Name, c.Index, c.Type}
					if v, ok := r.Value.Get(); ok {
						s.gauge(metric.value, v, labels...)
					}
					if r.Thresholds == nil || r.Thresholds.Unit != metric.thresholdUnit {
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
	for _, shelf := range shelves(snapshot) {
		for _, c := range shelf.components {
			if !c.IsBay() || len(c.SASAddresses) == 0 {
				continue
			}
			slot := ""
			if n, ok := c.SlotNumber.Get(); ok {
				slot = number(n)
			}
			for _, address := range c.SASAddresses {
				s.gauge(descMapping, 1,
					shelf.id, slot, c.Index, address, c.Device.Or(""), c.Map.Or(""))
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
