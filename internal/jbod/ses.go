// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// This file is the enclosure itself rather than the bays in it: every
// element the shelf declares, the state it reports for each of them, the
// sensors and their thresholds, and the two verdicts that must never be
// confused — what the hardware says about itself, and how much of it we
// managed to read (ROADMAP 5).

// HealthLevel is a component or enclosure condition, ordered by severity.
//
// Absent and unknown are levels of their own on purpose. A bay with no
// power supply in it is not a healthy power supply, and an element whose
// status could not be read is not a healthy element either; folding either
// into "ok" is how a dead PSU becomes invisible.
type HealthLevel string

const (
	// HealthOK is an element the enclosure reports as OK.
	HealthOK HealthLevel = "ok"
	// HealthWarning is the SES "noncritical" condition.
	HealthWarning HealthLevel = "warning"
	// HealthCritical is the SES "critical" condition.
	HealthCritical HealthLevel = "critical"
	// HealthUnrecoverable is the SES "unrecoverable" condition.
	HealthUnrecoverable HealthLevel = "unrecoverable"
	// HealthAbsent is an element the enclosure declares but reports as not
	// installed.
	HealthAbsent HealthLevel = "absent"
	// HealthUnknown is an element whose condition was not reported, was
	// reported as unsupported or not available, or could not be read.
	HealthUnknown HealthLevel = "unknown"
)

// HealthLevels is every level, in the order the tables and the metrics
// render them, so a level that drops to zero keeps its series.
var HealthLevels = []HealthLevel{HealthOK, HealthWarning, HealthCritical, HealthUnrecoverable, HealthAbsent, HealthUnknown}

// severity orders the conditions that are statements about health. Absent
// and unknown are not statements about health and are not ranked here; see
// worstOf.
var severity = map[HealthLevel]int{
	HealthOK:            0,
	HealthWarning:       1,
	HealthCritical:      2,
	HealthUnrecoverable: 3,
}

// healthOfStatus maps the element status codes of the SES status pages, as
// sg_ses spells them, onto a condition.
//
// "No access allowed" is the code an I/O module returns for an element it
// does not own, which on a two-module chassis is half the shelf; it is
// unknown, exactly as it is for a bay (ROADMAP 4).
func healthOfStatus(status string) HealthLevel {
	switch strings.Join(strings.Fields(strings.ToLower(status)), " ") {
	case "ok":
		return HealthOK
	case "noncritical", "non-critical", "non critical":
		return HealthWarning
	case "critical":
		return HealthCritical
	case "unrecoverable":
		return HealthUnrecoverable
	case "not installed", "not present":
		return HealthAbsent
	default:
		// "Unsupported", "Unknown", "Not available", "No access allowed"
		// and anything this package has not seen.
		return HealthUnknown
	}
}

// worstOf returns the most severe condition among the levels that say
// something about health.
//
// Absent and unknown are skipped rather than ranked: a shelf whose thirty
// far-side bays read as unknown must not report its overall condition as
// unknown while its fans and power supplies are answering OK. They are not
// discarded either — ComponentSummary counts them, and CollectionStatus
// says whether the poll was complete, which is where an operator sees that
// part of the shelf was not readable. When nothing was readable, the answer
// is unknown, because then there is no observation to report.
func worstOf(levels []HealthLevel) HealthLevel {
	worst := HealthUnknown
	rank := -1
	for _, level := range levels {
		n, ok := severity[level]
		if !ok || n <= rank {
			continue
		}
		worst, rank = level, n
	}
	return worst
}

// Thresholds are the limits the enclosure declares for a sensor. They are
// read from the Threshold In page and never written (ROADMAP 5).
//
// Unit is what the numbers are in, and it is not always the unit of the
// reading: a temperature limit is in degrees, but the page gives voltage and
// current limits as a percentage of the sensor's nominal value — high limits
// above it, low limits below it — and the nominal value is on no page.
type Thresholds struct {
	HighCritical Optional[float64] `json:"high_critical"`
	HighWarning  Optional[float64] `json:"high_warning"`
	LowWarning   Optional[float64] `json:"low_warning"`
	LowCritical  Optional[float64] `json:"low_critical"`
	Unit         string            `json:"unit"`
}

// Reading kinds and their units.
const (
	ReadingTemperature = "temperature"
	ReadingSpeed       = "speed"
	ReadingVoltage     = "voltage"
	ReadingCurrent     = "current"

	UnitCelsius = "celsius"
	UnitRPM     = "rpm"
	UnitVolts   = "volts"
	UnitAmps    = "amps"
	// UnitPercentOfNominal is the unit of voltage and current thresholds:
	// an offset from the sensor's nominal value.
	UnitPercentOfNominal = "percent_of_nominal"
)

// Reading is one sensor value with everything needed to judge it: the unit,
// where it came from, when it was read and whether it was reported at all
// (ROADMAP 3).
//
// Value is absent when the element declares the reading but did not report
// one. That is not zero degrees and not a stopped fan.
type Reading struct {
	Kind  string            `json:"kind"`
	Unit  string            `json:"unit"`
	Value Optional[float64] `json:"value"`
	// Source is the command the value came from, so a number in a report
	// can be checked against the hardware by hand.
	Source string `json:"source"`
	// ReadAt is when the page this value came from was read.
	ReadAt time.Time `json:"read_at"`
	// Thresholds are the limits for this reading, when the enclosure
	// declares them for this element.
	Thresholds *Thresholds `json:"thresholds,omitempty"`
	// Err is why the value is absent, when the page itself failed.
	Err Optional[string] `json:"error"`
}

// Component is one SES element of one enclosure: a bay, a power supply, a
// fan, a sensor, an I/O module.
type Component struct {
	Enclosure   string           `json:"enclosure"`
	EnclosureID Optional[string] `json:"enclosure_id"`
	// Index is the "[type,element]" address sg_ses prints and takes.
	Index     string `json:"index"`
	TypeIndex int64  `json:"type_index"`
	// Element is the element number within its type; the overall element of
	// a type is never listed as a component.
	Element int64 `json:"element"`
	// Type is the SES element type, normalized to lower case.
	Type string `json:"type"`
	// Name is the element descriptor the enclosure publishes ("FAN PSU A").
	Name         string           `json:"name"`
	Subenclosure Optional[int64]  `json:"subenclosure"`
	Status       Optional[string] `json:"status"`
	Health       HealthLevel      `json:"health"`
	// Flags are the one-bit status fields as the enclosure reports them.
	// They are data, not a verdict: Health comes from the status code.
	Flags map[string]bool `json:"flags,omitempty"`
	// Readings are the values this element reports.
	Readings []Reading `json:"readings,omitempty"`
	// SASAddresses are the addresses of this element from the Additional
	// Element Status page, which is what ties a bay to a disk.
	SASAddresses []string `json:"sas_addresses,omitempty"`
	// SlotNumber is the bay number the enclosure reports for this element.
	SlotNumber Optional[int64] `json:"slot_number"`
	// Device and Map are the disk in this bay, joined from the sysfs slot
	// walk; both are absent for elements that are not bays.
	Device Optional[string] `json:"device"`
	Map    Optional[string] `json:"map"`
	// Declared marks an element the Configuration page declares but no
	// status page reported, so it is listed rather than silently missing.
	Declared bool `json:"declared_only"`
	// Err explains a component that could not be read.
	Err Optional[string] `json:"error"`
}

// Reading returns the reading of the given kind, if the element reports one.
func (c Component) Reading(kind string) (Reading, bool) {
	for _, r := range c.Readings {
		if r.Kind == kind {
			return r, true
		}
	}
	return Reading{}, false
}

// IsBay reports whether this element is a disk bay, which is the only kind
// of element the slot walk can be joined to.
func (c Component) IsBay() bool {
	return c.Type == "array device slot" || c.Type == "device slot"
}

// PageStatus is the outcome of reading one SES page.
//
// It is what separates "the enclosure is in trouble" from "we did not
// manage to ask": a page that failed is recorded here, and the readings it
// would have carried stay absent instead of defaulting (ROADMAP 5).
type PageStatus struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	OK      bool   `json:"ok"`
	// Required marks a page the report cannot be complete without. The
	// Threshold In page is optional: an enclosure that does not implement
	// it is not an enclosure we failed to read.
	Required   bool             `json:"required"`
	Generation Optional[string] `json:"generation"`
	Err        Optional[string] `json:"error"`
}

// HardwareStatus is what the enclosure says about itself on the Enclosure
// Status page. Every bit is optional: a page that did not answer reports
// nothing, not five zeros.
type HardwareStatus struct {
	Level            HealthLevel      `json:"level"`
	InvalidOperation Optional[bool]   `json:"invalid_operation"`
	Info             Optional[bool]   `json:"info"`
	NonCritical      Optional[bool]   `json:"non_critical"`
	Critical         Optional[bool]   `json:"critical"`
	Unrecoverable    Optional[bool]   `json:"unrecoverable"`
	Err              Optional[string] `json:"error"`
}

// CollectionStatus is how complete the poll was, which is not a statement
// about the hardware at all (ROADMAP 5).
type CollectionStatus struct {
	// Complete is true when every required page answered and the pages
	// agreed on the generation code.
	Complete bool         `json:"complete"`
	ReadAt   time.Time    `json:"read_at"`
	Pages    []PageStatus `json:"pages"`
	// Generation is the configuration generation code the pages reported.
	Generation Optional[string] `json:"generation"`
	// GenerationChanged reports that the pages of this pass did not agree
	// on it, so they describe different configurations and the report is a
	// mixture. The pass is not repeated; the mixture is reported as such.
	GenerationChanged bool `json:"generation_changed"`
	// Missing counts elements the Configuration page declares that no
	// status page reported.
	Missing int `json:"missing_components"`
}

// ComponentSummary is the roll-up of one shelf's elements.
type ComponentSummary struct {
	Total int `json:"total"`
	// Level is the worst condition actually observed; see worstOf.
	Level HealthLevel `json:"level"`
	// Counts is how many elements are in each condition.
	Counts map[HealthLevel]int `json:"counts"`
}

// Count returns the number of elements in one condition.
func (s ComponentSummary) Count(level HealthLevel) int { return s.Counts[level] }

// EnclosureStatus is one shelf's health report: what it says about itself,
// what its elements report, and how much of that we managed to read.
type EnclosureStatus struct {
	Enclosure   string           `json:"enclosure"`
	EnclosureID Optional[string] `json:"enclosure_id"`
	// Address is what to pass to a command to name this shelf, and
	// StableID says whether that address survives a reboot.
	Address  string `json:"address"`
	StableID bool   `json:"stable_id"`
	// Vendor, Model and Revision come from the Configuration page's primary
	// subenclosure when it answered.
	Subenclosures []Subenclosure   `json:"subenclosures,omitempty"`
	Hardware      HardwareStatus   `json:"hardware"`
	Components    []Component      `json:"components"`
	Summary       ComponentSummary `json:"summary"`
	Collection    CollectionStatus `json:"collection"`
}

// Subenclosure is one subenclosure of a shelf as the Configuration page
// declares it: on a two-module chassis, one per I/O module.
type Subenclosure struct {
	ID        Optional[int64]  `json:"id"`
	Primary   bool             `json:"primary"`
	LogicalID Optional[string] `json:"logical_id"`
	Vendor    Optional[string] `json:"vendor"`
	Product   Optional[string] `json:"product"`
	Revision  Optional[string] `json:"revision"`
}

// Sensors returns the elements of this shelf that report a value.
func (s EnclosureStatus) Sensors() []Component {
	var sensors []Component
	for _, c := range s.Components {
		if len(c.Readings) > 0 {
			sensors = append(sensors, c)
		}
	}
	return sensors
}

// Level is the condition to show for the shelf: the worse of what the
// enclosure reports about itself and what its elements report.
//
// The two are kept apart everywhere else — a shelf whose status page says
// CRIT while every element reads OK is a different situation from the
// reverse — but a single line has to say one thing, and it says the worse
// of the two.
//
// With one exception, which is the whole point of having an unknown level:
// a headline of "ok" is a claim that the shelf is fine, so it is only made
// when both halves answered. If either of them is unknown and nothing worse
// was observed, the headline is unknown as well. Unknown still never masks
// a critical: a shelf with a failed power supply and an unreadable status
// page is critical, not unknown.
func (s EnclosureStatus) Level() HealthLevel {
	level := worstOf([]HealthLevel{s.Hardware.Level, s.Summary.Level})
	if level == HealthOK && (s.Hardware.Level == HealthUnknown || s.Summary.Level == HealthUnknown) {
		return HealthUnknown
	}
	return level
}

// sesPage is one page read of one shelf.
type sesPage struct {
	name     string
	args     []string
	required bool
}

// The pages one pass reads. The join is the expensive one and the only one
// that carries element descriptors and Additional Element Status together;
// the other three are small.
var sesPages = struct {
	config, status, join, thresholds sesPage
}{
	config: sesPage{name: "configuration", args: []string{"--page=cf"}, required: true},
	status: sesPage{name: "enclosure status", args: []string{"--page=es"}, required: true},
	join:   sesPage{name: "join", args: []string{"--join"}, required: true},
	// The Threshold In page is read raw and decoded against the
	// configuration; see parseThresholdPage for why sg_ses's own decoding
	// of it cannot be used.
	thresholds: sesPage{name: "threshold in", args: []string{"--page=th", "--raw"}, required: false},
}

// Inspect reads the health, components and sensors of the given enclosures.
//
// It reads four diagnostic pages per shelf and walks sysfs once to join the
// bays to their disks. Nothing here writes: reading a threshold is a read,
// and changing one is 1.4 (ROADMAP 7).
func (c *Client) Inspect(ctx context.Context, enclosures []Enclosure) ([]EnclosureStatus, error) {
	p := newProblems(c.logger)
	// The slot walk is cheap next to four SES pages, and it is what turns
	// a bay's SAS address into the disk an operator can act on.
	slots := c.slots(ctx, enclosures, p)
	result := c.inspect(ctx, enclosures, slots, p)
	if err := p.err(); err != nil {
		return result, err
	}
	return result, ctx.Err()
}

// inspect reads every shelf, shelves in parallel and pages in sequence.
//
// The pages of one shelf go through the same expander and the same SES
// processor, so there is nothing to win by running them at once and a
// queue to avoid; different shelves are different devices.
func (c *Client) inspect(ctx context.Context, enclosures []Enclosure, slots []Slot, p *problems) []EnclosureStatus {
	if len(enclosures) == 0 {
		return nil
	}
	result := make([]EnclosureStatus, len(enclosures))
	forEach(ctx, c.concurrency, len(enclosures), func(i int) {
		result[i] = c.inspectOne(ctx, enclosures[i], slots, p)
	})
	return result
}

// read runs one page and records how it went.
func (c *Client) read(ctx context.Context, enc Enclosure, page sesPage, p *problems) (string, PageStatus) {
	args := append(slices.Clone(page.args), enc.Device)
	out, err := c.exec(ctx, "sg_ses", args...)
	status := PageStatus{
		Name:     page.name,
		Command:  "sg_ses " + strings.Join(args, " "),
		OK:       err == nil,
		Required: page.required,
	}
	if err != nil {
		// A page that did not answer is counted and reported, and the pass
		// continues: losing the fans because the threshold page is not
		// implemented is exactly the failure this is built to avoid.
		status.Err = Some(err.Error())
		p.note(CollectorComponents, fmt.Errorf("%s: %w", status.Command, err))
		return "", status
	}
	status.Generation = pageGeneration(out)
	return out, status
}

// pageGeneration reads the generation code of a page.
//
// It scans line by line, because a tag value ends at a comma or at a run of
// two spaces and a newline is neither: reading the whole page at once works
// only as long as sg_ses indents the line after the generation code, and a
// value that swallowed the next line would compare unequal to the other
// pages' and report a configuration change that did not happen.
func pageGeneration(out string) Optional[string] {
	for line := range strings.Lines(out) {
		if v, ok := tag(line, "generation code"); ok {
			return Some(v)
		}
	}
	return None[string]()
}

// inspectOne builds the report for one shelf.
func (c *Client) inspectOne(ctx context.Context, enc Enclosure, slots []Slot, p *problems) EnclosureStatus {
	address, stable := enc.Ref()
	report := EnclosureStatus{
		Enclosure:   enc.Slot,
		EnclosureID: enc.ID,
		Address:     address,
		StableID:    stable,
		Collection:  CollectionStatus{ReadAt: time.Now()},
	}
	configOut, configPage := c.read(ctx, enc, sesPages.config, p)
	joinOut, joinPage := c.read(ctx, enc, sesPages.join, p)
	statusOut, statusPage := c.read(ctx, enc, sesPages.status, p)

	config := parseConfiguration(configOut)
	elements := parseJoinElements(joinOut)
	report.Subenclosures = subenclosures(config)
	report.Hardware = hardwareStatus(parseEnclosureStatus(statusOut), statusPage)

	// The threshold page is read last and only for the types that have
	// sensors: on a shelf with none there is nothing it could add.
	var thresholds map[string]sesThreshold
	thresholdPage := PageStatus{Name: sesPages.thresholds.name, Required: false}
	if hasSensors(elements) {
		var out string
		out, thresholdPage = c.read(ctx, enc, sesPages.thresholds, p)
		if thresholdPage.OK {
			decoded, err := parseThresholdPage(out, config)
			if decoded.Generation != "" {
				thresholdPage.Generation = Some(decoded.Generation)
			}
			if err != nil {
				// The page answered and could not be used. That is a
				// failure of this collection, not of the shelf, and it
				// is counted as one.
				thresholdPage.Err = Some("the page answered and was not decoded: " + err.Error())
				p.note(CollectorComponents, fmt.Errorf("%s: %w", thresholdPage.Command, err))
			} else {
				thresholds = decoded.Limits
			}
		}
	} else {
		thresholdPage.Command = "sg_ses " + strings.Join(sesPages.thresholds.args, " ") + " " + enc.Device
		thresholdPage.OK = true
		thresholdPage.Err = Some("the shelf reports no sensor elements")
	}
	report.Collection.Pages = []PageStatus{configPage, joinPage, statusPage, thresholdPage}

	report.Components = c.components(enc, config, elements, thresholds, slots, report.Collection.ReadAt)
	report.Collection.Missing = countMissing(config, elements)
	report.Summary = summarize(report.Components)
	report.Collection.Generation, report.Collection.GenerationChanged = generation(report.Collection.Pages)
	report.Collection.Complete = complete(report.Collection)
	if report.Collection.GenerationChanged {
		// A configuration change between two page reads is a collection
		// problem, not a hardware fault, and it is counted as one.
		p.note(CollectorComponents, fmt.Errorf(
			"%s: the SES pages of this pass report different generation codes, so the report mixes two configurations", enc.Device))
	}
	return report
}

// hasSensors reports whether any element carries a reading whose page has
// thresholds.
func hasSensors(elements []sesElement) bool {
	for _, e := range elements {
		switch e.Type {
		case sesTypeTemperature, sesTypeVoltage, sesTypeCurrent:
			return true
		}
	}
	return false
}

// subenclosures converts the Configuration page's enclosure descriptors.
func subenclosures(config sesConfig) []Subenclosure {
	var result []Subenclosure
	for _, s := range config.Subenclosures {
		result = append(result, Subenclosure(s))
	}
	return result
}

// hardwareStatus turns the five condition bits into a level.
//
// A page that did not answer reports unknown, and the bits stay absent: the
// alternative is a shelf that reads as healthy because nobody could ask it.
func hardwareStatus(status sesEnclosureStatus, page PageStatus) HardwareStatus {
	result := HardwareStatus{
		Level:            HealthUnknown,
		InvalidOperation: status.InvalidOperation,
		Info:             status.Info,
		NonCritical:      status.NonCritical,
		Critical:         status.Critical,
		Unrecoverable:    status.Unrecoverable,
		Err:              page.Err,
	}
	switch {
	case status.Unrecoverable.Or(false):
		result.Level = HealthUnrecoverable
	case status.Critical.Or(false):
		result.Level = HealthCritical
	case status.NonCritical.Or(false):
		result.Level = HealthWarning
	case status.Critical.Present() || status.NonCritical.Present() || status.Unrecoverable.Present():
		// At least one condition bit was reported and none of them is set.
		result.Level = HealthOK
	}
	return result
}

// components turns the parsed elements into the component list of one
// shelf, joined to the thresholds and to the disks.
func (c *Client) components(enc Enclosure, config sesConfig, elements []sesElement,
	thresholds map[string]sesThreshold, slots []Slot, readAt time.Time,
) []Component {
	bays := baysOf(slots, enc.Slot)
	var result []Component
	for _, e := range elements {
		if e.Overall() {
			// The overall element of a type summarises the others; listing
			// it as a component is how a summary came to be published as a
			// stopped fan (ROADMAP 4).
			continue
		}
		component := Component{
			Enclosure:    enc.Slot,
			EnclosureID:  enc.ID,
			Index:        e.Index,
			TypeIndex:    e.TypeIndex,
			Element:      e.Element,
			Type:         e.Type,
			Name:         e.Descriptor,
			Subenclosure: subenclosureOf(config, e.TypeIndex),
			Status:       e.Status,
			Health:       HealthUnknown,
			SASAddresses: e.SASAddresses,
			SlotNumber:   e.SlotNumber,
		}
		if len(e.Flags) > 0 {
			component.Flags = e.Flags
		}
		if status, ok := e.Status.Get(); ok {
			component.Health = healthOfStatus(status)
		} else {
			component.Err = Some("the status page reported no condition for this element")
		}
		component.Readings = readingsOf(e, thresholds, readAt)
		if component.IsBay() {
			joinBay(&component, bays)
		}
		result = append(result, component)
	}
	result = append(result, missingComponents(enc, config, elements)...)
	slices.SortStableFunc(result, compareComponents)
	return result
}

// compareComponents orders the listing by type index and then by element,
// which is the order the enclosure declares them in.
func compareComponents(a, b Component) int {
	if a.TypeIndex != b.TypeIndex {
		return cmp.Compare(a.TypeIndex, b.TypeIndex)
	}
	return cmp.Compare(a.Element, b.Element)
}

// subenclosureOf returns the subenclosure a type index belongs to.
func subenclosureOf(config sesConfig, typeIndex int64) Optional[int64] {
	for _, t := range config.Types {
		if t.TypeIndex == typeIndex {
			return t.Subenclosure
		}
	}
	return None[int64]()
}

// readingsOf collects the values one element reported, with the unit, the
// source, the time and the thresholds that belong to each.
func readingsOf(e sesElement, thresholds map[string]sesThreshold, readAt time.Time) []Reading {
	source := "sg_ses --join"
	var readings []Reading
	add := func(kind, unit string, value Optional[float64]) {
		r := Reading{Kind: kind, Unit: unit, Value: value, Source: source, ReadAt: readAt}
		if t, ok := thresholds[e.Index]; ok {
			r.Thresholds = &Thresholds{
				HighCritical: t.HighCritical,
				HighWarning:  t.HighWarning,
				LowWarning:   t.LowWarning,
				LowCritical:  t.LowCritical,
				Unit:         t.Unit,
			}
		}
		if !value.Present() {
			r.Err = Some("the element declares this reading but reported no value")
		}
		readings = append(readings, r)
	}
	// A sensor element always gets its reading, present or not: an element
	// that stopped answering has to stay visible, the way a cooling element
	// without an RPM does (ROADMAP 4).
	switch e.Type {
	case "temperature sensor":
		add(ReadingTemperature, UnitCelsius, e.Temperature)
	case "voltage sensor":
		add(ReadingVoltage, UnitVolts, e.Voltage)
	case "current sensor":
		add(ReadingCurrent, UnitAmps, e.Current)
	case "cooling":
		add(ReadingSpeed, UnitRPM, optionalFloat(e.Speed))
	}
	// Elements of other types report a temperature too — a power supply and
	// an I/O module commonly do — and those are values, not noise.
	if e.Type != "temperature sensor" && e.Temperature.Present() {
		add(ReadingTemperature, UnitCelsius, e.Temperature)
	}
	if e.Type != "voltage sensor" && e.Voltage.Present() {
		add(ReadingVoltage, UnitVolts, e.Voltage)
	}
	if e.Type != "current sensor" && e.Current.Present() {
		add(ReadingCurrent, UnitAmps, e.Current)
	}
	if e.Type != "cooling" && e.Speed.Present() {
		add(ReadingSpeed, UnitRPM, optionalFloat(e.Speed))
	}
	return readings
}

// optionalFloat widens an optional integer reading.
func optionalFloat(v Optional[int64]) Optional[float64] {
	n, ok := v.Get()
	if !ok {
		return None[float64]()
	}
	return Some(float64(n))
}

// baysOf indexes the bays of one shelf by slot number and by name, which
// are the two things an SES element can be matched on.
type bayIndex struct {
	byNumber map[int64]Slot
	byName   map[string]Slot
}

func baysOf(slots []Slot, enclosure string) bayIndex {
	index := bayIndex{byNumber: map[int64]Slot{}, byName: map[string]Slot{}}
	for _, s := range slots {
		if s.Enclosure != enclosure {
			continue
		}
		if n, ok := s.Number.Get(); ok {
			if _, seen := index.byNumber[n]; !seen {
				index.byNumber[n] = s
			}
		}
		for _, name := range []string{s.Name, s.Label} {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" {
				continue
			}
			if _, seen := index.byName[key]; !seen {
				index.byName[key] = s
			}
		}
	}
	return index
}

// joinBay ties a bay element to the disk the kernel sees in it, which is
// the slot → SAS address → disk mapping (ROADMAP 5).
//
// The bay number the Additional Element Status page reports is the
// enclosure's own answer to "which bay is this", so when it is there it is
// the only thing matched on: a bay the enclosure calls 30 and that sysfs
// does not have is a bay whose disk we cannot name, not the disk that
// happens to sit in the slot with the element's ordinal. On a chassis whose
// element numbering and bay numbering are offset — the two-module shelf
// this is written for — guessing would attach the wrong disk to the right
// bay, which is worse than attaching none.
//
// The element number and the descriptor text are the fallbacks for
// enclosures that report no bay number at all. A component that matches
// nothing keeps its SAS address and gets no device.
func joinBay(component *Component, bays bayIndex) {
	attach := func(slot Slot) {
		component.Device, component.Map = slot.Device, slot.Map
		if !component.SlotNumber.Present() {
			component.SlotNumber = slot.Number
		}
	}
	if n, ok := component.SlotNumber.Get(); ok {
		if slot, found := bays.byNumber[n]; found {
			attach(slot)
		}
		return
	}
	if slot, ok := bays.byNumber[component.Element]; ok {
		attach(slot)
		return
	}
	name := strings.ToLower(strings.TrimSpace(component.Name))
	if name == "" {
		return
	}
	if slot, ok := bays.byName[name]; ok {
		attach(slot)
	}
}

// missingComponents lists the elements the Configuration page declares that
// no status page reported.
//
// They are listed rather than dropped: "the shelf says it has two power
// supplies and only one of them answered" is a finding, and a listing that
// simply shows one power supply hides it (ROADMAP 5).
func missingComponents(enc Enclosure, config sesConfig, elements []sesElement) []Component {
	reported := map[string]bool{}
	for _, e := range elements {
		reported[e.Index] = true
	}
	var result []Component
	for _, t := range config.Types {
		possible, ok := t.Possible.Get()
		if !ok {
			continue
		}
		for element := range possible {
			index := strconv.FormatInt(t.TypeIndex, 10) + "," + strconv.FormatInt(element, 10)
			if reported[index] {
				continue
			}
			result = append(result, Component{
				Enclosure:    enc.Slot,
				EnclosureID:  enc.ID,
				Index:        index,
				TypeIndex:    t.TypeIndex,
				Element:      element,
				Type:         t.Type,
				Name:         t.Text.Or(""),
				Subenclosure: t.Subenclosure,
				Health:       HealthUnknown,
				Declared:     true,
				Err: Some("declared by the configuration page and not reported by any status page; " +
					"this is a gap in what was read, not a statement about the element"),
			})
		}
	}
	return result
}

// countMissing counts the declared elements no status page reported.
func countMissing(config sesConfig, elements []sesElement) int {
	reported := map[string]bool{}
	for _, e := range elements {
		reported[e.Index] = true
	}
	missing := 0
	for _, t := range config.Types {
		possible, ok := t.Possible.Get()
		if !ok {
			continue
		}
		for element := range possible {
			if !reported[strconv.FormatInt(t.TypeIndex, 10)+","+strconv.FormatInt(element, 10)] {
				missing++
			}
		}
	}
	return missing
}

// summarize rolls the component conditions up.
func summarize(components []Component) ComponentSummary {
	summary := ComponentSummary{Total: len(components), Counts: map[HealthLevel]int{}}
	levels := make([]HealthLevel, 0, len(components))
	for _, c := range components {
		summary.Counts[c.Health]++
		levels = append(levels, c.Health)
	}
	summary.Level = worstOf(levels)
	return summary
}

// generation returns the generation code the pages agreed on, and whether
// they disagreed.
//
// The generation code changes when the enclosure's configuration does, so
// two pages reporting different ones describe two different shelves. The
// pass is not repeated over it — a shelf being reconfigured would repeat
// forever — and the report says so instead (ROADMAP 5).
func generation(pages []PageStatus) (Optional[string], bool) {
	var first Optional[string]
	changed := false
	for _, page := range pages {
		value, ok := page.Generation.Get()
		if !ok {
			continue
		}
		if !first.Present() {
			first = Some(value)
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(first.Or("")), strings.TrimSpace(value)) {
			changed = true
		}
	}
	return first, changed
}

// complete reports whether every required page answered and the pages
// agreed on the configuration they describe.
func complete(status CollectionStatus) bool {
	if status.GenerationChanged {
		return false
	}
	for _, page := range status.Pages {
		if page.Required && !page.OK {
			return false
		}
	}
	return true
}
