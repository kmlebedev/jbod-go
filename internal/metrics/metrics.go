// SPDX-License-Identifier: BSD-2-Clause

// Package metrics turns a collection snapshot into Prometheus metrics.
//
// It does no I/O and owns no state: what it publishes is decided by the
// snapshot it is handed, which keeps the exposition testable without a
// shelf. The exposition itself is not written by hand — the collector below
// hands const metrics to github.com/prometheus/client_golang, which owns the
// text and OpenMetrics formats, the escaping and the sorting.
//
// Every value is read at scrape time and belongs to a snapshot that is
// thrown away afterwards, so the series are built with
// prometheus.MustNewConstMetric rather than with the Gauge/Counter objects
// of direct instrumentation. That is what the client library's "writing
// exporters" guidance asks for, and it is also what keeps a disappeared
// shelf from leaving a stale series behind.
package metrics

import (
	"slices"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// Options controls what the collector publishes.
type Options struct {
	// Deprecated keeps the pre-1.1 series in the output. It defaults to
	// off in the zero value, and the exporter turns it on, because the
	// migration period is the default and dropping a series under a
	// running dashboard is not (ROADMAP 4).
	Deprecated bool
}

// Descriptors of the core series. They are package-level because a
// descriptor is immutable and shared by every scrape; building them once
// also means the help text and the label set of a series exist in exactly
// one place.
var (
	descEnclosureCount = prometheus.NewDesc(
		"number_of_enclosures",
		"Number of enclosures",
		nil, nil)
	// The join target for every other series: the rest are labelled with
	// the SCSI address, which is a location and gets reassigned, so a
	// dashboard that needs a stable identity joins on enclosure and reads
	// enclosure_id here. id_source says whether that identity is really
	// stable or just the address again (ROADMAP 3).
	descEnclosureInfo = prometheus.NewDesc(
		"jbod_enclosure_info",
		"Identity of each enclosure; id_source is logical, serial or address",
		[]string{"enclosure", "enclosure_id", "id_source", "vendor", "model", "revision", "serial"}, nil)
	descSlots = prometheus.NewDesc(
		"jbod_enclosure_slots",
		"Slots per enclosure by occupancy",
		[]string{"enclosure", "enclosure_id", "occupancy"}, nil)
	descSlotTemperature = prometheus.NewDesc(
		"jbod_slot_temperature",
		"Enclosure number, slot position and temperature",
		[]string{"slot", "enclosure"}, nil)
	descFanSpeed = prometheus.NewDesc(
		"jbod_fan_speed_rpm",
		"Speed of a cooling element, addressed by enclosure and component",
		[]string{"enclosure", "enclosure_id", "component", "component_id"}, nil)
	descFanRPM = prometheus.NewDesc(
		"jbod_fan_rpm",
		"DEPRECATED, replaced by jbod_fan_speed_rpm: the labels omit the enclosure, so identical fans of two shelves overwrite each other. Removal is planned for 2.0.",
		[]string{"device", "slot"}, nil)
	descUp = prometheus.NewDesc(
		"jbod_up",
		"Whether the last collection completed",
		nil, nil)
	descScrapeDuration = prometheus.NewDesc(
		"jbod_scrape_duration_seconds",
		"Duration of the last collection",
		nil, nil)
	descScrapeErrors = prometheus.NewDesc(
		"jbod_scrape_errors_total",
		"Failed collection operations per collector",
		[]string{"collector"}, nil)
)

// descriptors is every descriptor this package can publish, for Describe.
//
// The collector is a checked one: it announces its descriptors up front, so
// the registry can reject a second collector that would publish the same
// series and catch a label set that changed by accident.
var descriptors = append([]*prometheus.Desc{
	descEnclosureCount, descEnclosureInfo, descSlots, descSlotTemperature,
	descFanSpeed, descFanRPM, descUp, descScrapeDuration, descScrapeErrors,
}, append(healthDescriptors, sasDescriptors...)...)

// Collector publishes one snapshot.
//
// It is deliberately a value over an already collected snapshot rather than
// something that talks to the hardware from Collect: the scrape budget, the
// deduplication of overlapping scrapes and the cache live in the exporter,
// which needs a context that prometheus.Collector cannot pass (B3).
type Collector struct {
	snapshot jbod.Snapshot
	// errorTotals carries the cumulative per-collector failure counts,
	// because jbod_scrape_errors_total is a counter and must not go
	// backwards between scrapes; the exporter keeps them across passes.
	errorTotals map[string]int
	opts        Options
}

// NewCollector returns a collector over s. Register it in a registry of its
// own per scrape, or gather it directly with prometheus/testutil.
func NewCollector(s jbod.Snapshot, errorTotals map[string]int, opts Options) *Collector {
	return &Collector{snapshot: s, errorTotals: errorTotals, opts: opts}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range descriptors {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	s := &sink{ch: ch, seen: map[key]struct{}{}}
	collectEnclosures(s, c.snapshot)
	collectSlots(s, c.snapshot)
	collectEnclosureHealth(s, c.snapshot)
	collectComponents(s, c.snapshot)
	collectSensors(s, c.snapshot)
	collectMapping(s, c.snapshot)
	collectTemperatures(s, c.snapshot)
	collectFans(s, c.snapshot, c.opts)
	collectPHYs(s, c.snapshot)
	collectCollection(s, c.snapshot)
	collectHealth(s, c.snapshot, c.errorTotals)
}

// key identifies one series: a descriptor and its label values.
type key struct {
	desc   *prometheus.Desc
	labels string
}

// sink is where the collect* functions put their series.
//
// It exists for one reason: a registry rejects two metrics with the same
// name and the same labels, and a gather that fails takes the whole scrape
// with it. The old text encoder printed both lines. Hardware that reports
// two elements with identical descriptions is not hypothetical — that is
// exactly the bug jbod_fan_speed_rpm was introduced for — so the duplicate
// is dropped here instead of spoiling a scrape.
type sink struct {
	ch   chan<- prometheus.Metric
	seen map[key]struct{}
}

func (s *sink) gauge(d *prometheus.Desc, v float64, labels ...string) {
	s.metric(d, prometheus.GaugeValue, v, labels...)
}

func (s *sink) counter(d *prometheus.Desc, v float64, labels ...string) {
	s.metric(d, prometheus.CounterValue, v, labels...)
}

func (s *sink) metric(d *prometheus.Desc, kind prometheus.ValueType, v float64, labels ...string) {
	k := key{desc: d}
	for _, l := range labels {
		// The separator cannot occur in a label value read from the
		// hardware, so two different label sets cannot collide here.
		k.labels += l + "\x00"
	}
	if _, dup := s.seen[k]; dup {
		return
	}
	s.seen[k] = struct{}{}
	s.ch <- prometheus.MustNewConstMetric(d, kind, v, labels...)
}

// collectEnclosures publishes the identity of each shelf as an info metric.
func collectEnclosures(s *sink, snapshot jbod.Snapshot) {
	s.gauge(descEnclosureCount, float64(len(snapshot.Enclosures)))
	for _, e := range snapshot.Enclosures {
		id, _ := e.Ref()
		s.gauge(descEnclosureInfo, 1,
			e.Slot, id, e.IDSource(),
			e.Vendor.Or(""), e.Model.Or(""), e.Revision.Or(""), e.Serial.Or(""))
	}
}

// collectSlots counts the bays per state.
//
// The three states are reported separately so a shelf that lost a drive
// shows up as one fewer occupied and one more empty, rather than as a slot
// that silently stopped existing. A slot that could not be read is its own
// state and never folded into empty (ROADMAP 4).
func collectSlots(s *sink, snapshot jbod.Snapshot) {
	type slotKey struct {
		enclosure, id, occupancy string
	}
	counts := map[slotKey]int{}
	var order []slotKey
	ids := enclosureIDs(snapshot)
	// Every enclosure gets a series for every state, so a count going to
	// zero is visible instead of the series disappearing.
	for _, e := range snapshot.Enclosures {
		for _, occupancy := range []jbod.Occupancy{jbod.OccupancyOccupied, jbod.OccupancyEmpty, jbod.OccupancyUnavailable} {
			k := slotKey{e.Slot, ids[e.Slot], string(occupancy)}
			if _, seen := counts[k]; !seen {
				counts[k] = 0
				order = append(order, k)
			}
		}
	}
	for _, slot := range snapshot.Slots {
		k := slotKey{slot.Enclosure, ids[slot.Enclosure], string(slot.Occupancy)}
		if _, seen := counts[k]; !seen {
			order = append(order, k)
		}
		counts[k]++
	}
	for _, k := range order {
		s.gauge(descSlots, float64(counts[k]), k.enclosure, k.id, k.occupancy)
	}
}

func collectTemperatures(s *sink, snapshot jbod.Snapshot) {
	// Match the original gauge-vector behaviour: the last value wins for
	// duplicate labels, which the sink alone cannot do because it sees the
	// first one first.
	type tempKey struct{ slot, enclosure string }
	temps := map[tempKey]int64{}
	var order []tempKey
	for _, d := range snapshot.Disks {
		n, ok := d.Temperature.Get()
		if !ok {
			continue
		}
		k := tempKey{d.Slot, d.Enclosure}
		if _, seen := temps[k]; !seen {
			order = append(order, k)
		}
		temps[k] = n
	}
	for _, k := range order {
		s.gauge(descSlotTemperature, float64(temps[k]), k.slot, k.enclosure)
	}
}

// collectFans publishes the corrected fan series, and optionally the old one.
//
// jbod_fan_rpm labels a fan with its description and its SES index only, so
// "Fan A" at index 2,0 means the same thing on every shelf in the rack and
// the second enclosure overwrites the first. There is no way to fix that in
// place without changing what the existing series means, so the corrected
// metric is a new name carrying the enclosure as well, and the old one stays
// until 2.0 (ROADMAP 4).
func collectFans(s *sink, snapshot jbod.Snapshot, opts Options) {
	ids := enclosureIDs(snapshot)
	type fanKey struct {
		enclosure, id, component, index string
	}
	speeds := map[fanKey]int64{}
	var order []fanKey
	for _, f := range snapshot.Fans {
		// A cooling element that answered without an RPM reading gets no
		// series: a missing value is not zero, and a zero here reads as a
		// stopped fan.
		speed, ok := f.Speed.Get()
		if !ok {
			continue
		}
		id := ids[f.Slot]
		if id == "" {
			id = f.Serial.Or(f.Slot)
		}
		k := fanKey{f.Slot, id, f.Description, f.Index}
		if _, seen := speeds[k]; !seen {
			order = append(order, k)
		}
		speeds[k] = speed
	}
	for _, k := range order {
		s.gauge(descFanSpeed, float64(speeds[k]), k.enclosure, k.id, k.component, k.index)
	}
	if !opts.Deprecated {
		return
	}
	type oldKey struct{ device, slot string }
	old := map[oldKey]int64{}
	var oldOrder []oldKey
	for _, f := range snapshot.Fans {
		speed, ok := f.Speed.Get()
		if !ok {
			continue
		}
		k := oldKey{f.Description, f.Index}
		if _, seen := old[k]; !seen {
			oldOrder = append(oldOrder, k)
		}
		old[k] = speed
	}
	for _, k := range oldOrder {
		s.gauge(descFanRPM, float64(old[k]), k.device, k.slot)
	}
}

// collectHealth publishes the health of the scrape itself: a partial
// collection is reported through these series instead of an HTTP error (B5).
func collectHealth(s *sink, snapshot jbod.Snapshot, errorTotals map[string]int) {
	s.gauge(descUp, boolean(snapshot.Up))
	s.gauge(descScrapeDuration, snapshot.Duration.Seconds())
	for _, name := range collectors(errorTotals) {
		s.counter(descScrapeErrors, float64(errorTotals[name]), name)
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

// boolean renders a flag as the 0 or 1 the exposition expects.
func boolean(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// number renders an integer label value, for the slot numbers of the
// mapping series.
func number(v int64) string { return strconv.FormatInt(v, 10) }
