// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// This file parses the SES diagnostic pages as sg_ses prints them: the
// Configuration page, the Enclosure Status page, the join of Enclosure
// Status, Element Descriptor and Additional Element Status, and the
// Threshold In page, which is decoded from its raw bytes (ROADMAP 5).
//
// Everything here is a pure function of the text it is given, like the rest
// of parse.go, so the pages can be exercised from fixtures without a shelf.
//
// One rule runs through all of it: a field that is missing, unreadable or
// spelled in a way this parser does not know stays absent. sg_ses is a text
// interface that has changed spelling between releases and prints different
// shapes for different element types, and the failure that matters here is
// not an unparsed line — it is an unparsed line that becomes a zero
// temperature or a healthy power supply (ROADMAP 5).

// tag returns the value that follows "name:" or "name=" on a line.
//
// The value ends at a comma or at a run of two or more spaces, which is how
// sg_ses separates the fields it puts on one line:
//
//	Element type: Array device slot, subenclosure id: 0
//	enclosure vendor: HGST      product: H4060-J      rev: 4013
//
// The name is matched case-insensitively because the same field is spelled
// differently across sg_ses versions and element types.
func tag(line, name string) (string, bool) {
	i := strings.Index(strings.ToLower(line), strings.ToLower(name))
	if i < 0 {
		return "", false
	}
	rest := strings.TrimLeft(line[i+len(name):], " \t")
	if rest == "" || (rest[0] != ':' && rest[0] != '=') {
		return "", false
	}
	rest = strings.TrimSpace(rest[1:])
	if j := strings.IndexByte(rest, ','); j >= 0 {
		rest = rest[:j]
	}
	if loc := twoSpaces.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	rest = strings.TrimSpace(rest)
	return rest, rest != ""
}

var twoSpaces = regexp.MustCompile(`\s{2,}`)

// intTag returns the first integer of the value of a tag, so
// "Subenclosure identifier: 0 [primary]" yields 0.
func intTag(line, name string) (int64, bool) {
	value, ok := tag(line, name)
	if !ok {
		return 0, false
	}
	digits := number.FindString(value)
	if digits == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	return n, err == nil
}

// sesTypeDescriptor is one entry of the Configuration page type descriptor
// list: an element type, how many elements of it the enclosure declares, and
// which subenclosure they belong to.
//
// TypeIndex is the position in that list, which is the first half of the
// "[type,element]" index sg_ses prints everywhere else and the only way to
// address an element.
type sesTypeDescriptor struct {
	TypeIndex    int64
	Subenclosure Optional[int64]
	Type         string
	Possible     Optional[int64]
	Text         Optional[string]
}

// sesSubenclosure is one entry of the Configuration page enclosure
// descriptor list.
type sesSubenclosure struct {
	ID        Optional[int64]
	Primary   bool
	LogicalID Optional[string]
	Vendor    Optional[string]
	Product   Optional[string]
	Revision  Optional[string]
}

// sesConfig is the Configuration page: what the enclosure says it has,
// before anything is read about the state of it.
type sesConfig struct {
	Generation    Optional[string]
	Subenclosures []sesSubenclosure
	Types         []sesTypeDescriptor
}

// parseConfiguration reads "sg_ses --page=cf" output.
//
// The type descriptor list is what gives every element its type index, so
// the entries are numbered in the order they appear: that order is the
// index space of the whole page set, and an entry this parser skipped would
// shift every element after it.
func parseConfiguration(out string) sesConfig {
	var cfg sesConfig
	inTypes := false
	for line := range strings.Lines(out) {
		lower := strings.ToLower(line)
		if !cfg.Generation.Present() {
			if v, ok := tag(line, "generation code"); ok {
				cfg.Generation = Some(v)
			}
		}
		// The two headings of the page. They are matched on the start of
		// the line, because "number of type descriptor headers: 5" sits
		// inside an enclosure descriptor and is not a heading at all.
		switch trimmed := strings.ToLower(strings.TrimSpace(line)); {
		case strings.HasPrefix(trimmed, "type descriptor"):
			// Everything after this heading describes types, not shelves.
			inTypes = true
			continue
		case strings.HasPrefix(trimmed, "enclosure descriptor"):
			inTypes = false
			continue
		}
		if n, ok := intTag(line, "subenclosure identifier"); ok {
			cfg.Subenclosures = append(cfg.Subenclosures, sesSubenclosure{
				ID:      Some(n),
				Primary: strings.Contains(lower, "primary"),
			})
			continue
		}
		if len(cfg.Subenclosures) > 0 && !inTypes {
			sub := &cfg.Subenclosures[len(cfg.Subenclosures)-1]
			if v, ok := tag(line, "enclosure logical identifier (hex)"); ok {
				sub.LogicalID = Some(v)
			}
			if v, ok := tag(line, "enclosure vendor"); ok {
				sub.Vendor = Some(v)
			}
			if v, ok := tag(line, "product"); ok && sub.Vendor.Present() {
				sub.Product = Some(v)
			}
			if v, ok := tag(line, "rev"); ok && sub.Vendor.Present() {
				sub.Revision = Some(v)
			}
		}
		if name, ok := tag(line, "element type"); ok {
			entry := sesTypeDescriptor{
				TypeIndex:    int64(len(cfg.Types)),
				Type:         normalizeElementType(name),
				Subenclosure: From(intTag(line, "subenclosure id")),
				Possible:     From(intTag(line, "number of possible elements")),
			}
			cfg.Types = append(cfg.Types, entry)
			continue
		}
		// "number of possible elements" and "text" may sit on their own
		// lines under the type they belong to.
		if len(cfg.Types) > 0 {
			entry := &cfg.Types[len(cfg.Types)-1]
			if n, ok := intTag(line, "number of possible elements"); ok && !entry.Possible.Present() {
				entry.Possible = Some(n)
			}
			if v, ok := tag(line, "text"); ok && !entry.Text.Present() {
				entry.Text = Some(v)
			}
		}
	}
	return cfg
}

// sesEnclosureStatus is the summary the Enclosure Status page puts above the
// element list: the five condition bits of the enclosure and the generation
// code the whole page set has to agree on.
//
// Every bit is optional. A page that could not be read, or one whose header
// this parser does not recognise, must not report five zeros — that is a
// healthy enclosure, and it would be a fabricated one.
type sesEnclosureStatus struct {
	Generation       Optional[string]
	InvalidOperation Optional[bool]
	Info             Optional[bool]
	NonCritical      Optional[bool]
	Critical         Optional[bool]
	Unrecoverable    Optional[bool]
}

// statusBits are the five condition bits, with the spellings sg_ses uses.
// The names are matched case-insensitively and the separator may be "=" or
// ":", as everywhere else here.
var statusBits = []struct {
	names []string
	field func(*sesEnclosureStatus, Optional[bool])
}{
	{[]string{"INVOP"}, func(s *sesEnclosureStatus, v Optional[bool]) { s.InvalidOperation = v }},
	{[]string{"INFO"}, func(s *sesEnclosureStatus, v Optional[bool]) { s.Info = v }},
	{[]string{"NON-CRIT", "NONCRIT"}, func(s *sesEnclosureStatus, v Optional[bool]) { s.NonCritical = v }},
	{[]string{"CRIT"}, func(s *sesEnclosureStatus, v Optional[bool]) { s.Critical = v }},
	{[]string{"UNRECOV"}, func(s *sesEnclosureStatus, v Optional[bool]) { s.Unrecoverable = v }},
}

// bitTag reads one of the condition bits. "CRIT" must not be read out of
// "NON-CRIT", so the character before the name has to be a separator.
func bitTag(line, name string) (bool, bool) {
	lower, want := strings.ToLower(line), strings.ToLower(name)
	for i := 0; i+len(want) <= len(lower); i++ {
		if lower[i:i+len(want)] != want {
			continue
		}
		if i > 0 {
			switch prev := lower[i-1]; {
			case prev == '-', prev == '_', isDigit(prev),
				prev >= 'a' && prev <= 'z':
				continue
			}
		}
		v, ok := tag(line[i:], name)
		if !ok {
			continue
		}
		return v != "0", true
	}
	return false, false
}

// parseEnclosureStatus reads "sg_ses --page=es" output. Only the page
// header is taken from here: the elements themselves come from the join,
// which carries their descriptors as well.
func parseEnclosureStatus(out string) sesEnclosureStatus {
	var status sesEnclosureStatus
	for line := range strings.Lines(out) {
		if !status.Generation.Present() {
			if v, ok := tag(line, "generation code"); ok {
				status.Generation = Some(v)
			}
		}
		for _, bit := range statusBits {
			for _, name := range bit.names {
				if v, ok := bitTag(line, name); ok {
					bit.field(&status, Some(v))
					break
				}
			}
		}
	}
	return status
}

// elementTypes are the SES element type names, longest first so that
// "array device slot" is recognised before "device slot" and
// "scsi target port" before "scsi port/transceiver".
//
// They exist because the type is what decides how an element is read: a
// cooling element has an RPM, a temperature sensor has degrees, and an
// array device slot has a SAS address and a bay number.
var elementTypes = []string{
	"enclosure services controller electronics",
	"scc controller electronics",
	"uninterruptible power supply",
	"invalid operation reason",
	"scsi port/transceiver",
	"simple subenclosure",
	"array device slot",
	"scsi initiator port",
	"communication port",
	"temperature sensor",
	"scsi target port",
	"nonvolatile cache",
	"voltage sensor",
	"current sensor",
	"key pad entry",
	"sas connector",
	"audible alarm",
	"power supply",
	"sas expander",
	"device slot",
	"door lock",
	"unspecified",
	"enclosure",
	"language",
	"display",
	"cooling",
	"door",
}

// normalizeElementType folds an element type to the spelling this package
// uses: lower case, single spaces, no trailing "element".
func normalizeElementType(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimSuffix(s, " element")
	s = strings.TrimSuffix(s, " elements")
	return strings.TrimSpace(s)
}

// elementTypeIn finds a known element type inside a fragment of a line.
//
// sg_ses has printed the type of an element in more than one shape, and the
// shape is not something a parser should depend on: this package has seen
// "[3,0]  Element type: Cooling", "Fan A [2,0]  Cooling element" and a
// standalone "Element type: Cooling" header above the elements it covers.
// All three carry the same word, so that is what is looked for.
func elementTypeIn(s string) (string, bool) {
	normalized := normalizeElementType(s)
	if v, ok := tag(normalized, "element type"); ok {
		normalized = normalizeElementType(v)
	}
	for _, name := range elementTypes {
		if normalized == name {
			return name, true
		}
	}
	for _, name := range elementTypes {
		if strings.Contains(normalized, name) {
			return name, true
		}
	}
	return "", false
}

// elementTypeOf resolves the type named by a header, falling back to the
// type of the block the header sits in.
//
// A type sg_ses cannot name is still a type: it prints "vendor specific
// [0x81]" for the vendor range and a bare code for anything else it does
// not know, and shelves do declare such elements. Keeping the raw spelling
// is what stops those elements from inheriting the previous type — which
// would give them its type index, its readings and its place in the
// listing.
func elementTypeOf(text, fallback string) string {
	if name, ok := elementTypeIn(text); ok {
		return name
	}
	if raw, ok := tag(text, "element type"); ok {
		return normalizeElementType(raw)
	}
	return fallback
}

// isTypeHeader reports whether a line names an element type, whether or not
// this package knows the type.
func isTypeHeader(line string) bool {
	_, ok := tag(line, "element type")
	return ok
}

// sesElement is one element of the join output, before it is turned into a
// Component.
type sesElement struct {
	TypeIndex int64
	Element   int64
	// Index is the "[type,element]" address as sg_ses prints it, which is
	// also what "sg_ses --index=" takes.
	Index      string
	Descriptor string
	Type       string
	Status     Optional[string]
	// Flags are the one-bit fields of the element status, by their printed
	// name. They are kept as data: the health verdict comes from the status
	// code, and a bit whose meaning this package does not model must not
	// quietly become an alarm.
	Flags       map[string]bool
	Temperature Optional[float64]
	Speed       Optional[int64]
	Voltage     Optional[float64]
	Current     Optional[float64]
	// SASAddresses are the addresses the Additional Element Status page
	// reports for this element, without the attached ones: those belong to
	// the expander phy on the other side of the link.
	SASAddresses []string
	SlotNumber   Optional[int64]
}

// Overall reports whether this is the SES overall element of its type: a
// summary of every element of the type rather than one of them. It is
// counted but never listed as a component, for the same reason a
// "[3,-1] Fan stopped" summary must not be published as a stopped fan
// (ROADMAP 4).
func (e sesElement) Overall() bool { return e.Element < 0 }

var (
	// elementIndex matches the "[type,element]" address wherever it appears
	// on a line, with the text before it as the element descriptor.
	elementIndex = regexp.MustCompile(`^(.*?)\s*\[\s*(-?\d+)\s*,\s*(-?\d+)\s*\](.*)$`)
	// typeIndexOnly matches the "[ti=3]" form of a standalone type header.
	typeIndexOnly = regexp.MustCompile(`\[\s*ti\s*=\s*(-?\d+)\s*\]`)
	// elementOrdinal matches "Element 0 descriptor:", "Element 0
	// threshold:" and the other shapes sg_ses uses when the elements are
	// listed under a type header instead of carrying their own index.
	elementOrdinal = regexp.MustCompile(`(?i)^\s*element\s+(\d+)\b`)
	overallOrdinal = regexp.MustCompile(`(?i)^\s*overall\s+(?:descriptor|status|threshold)`)
	// flagPair matches one "Name=0" field of an element status line. The
	// fields are comma-separated and the line is split before matching:
	// a pattern that swallowed the separator would skip every second
	// field of "Hot swap=1, Fail=1, Requested on=1".
	flagPair = regexp.MustCompile(`^\s*([A-Za-z][^=]*?)\s*=\s*([01])\s*$`)
	// The readings. Each requires its unit, so a status bit that happens to
	// be called "Temperature warn" cannot be read as a temperature, and a
	// sensor that answered "<not available>" stays absent.
	temperatureReading = regexp.MustCompile(`(?i)(?:^|[^a-z])temperature\s*[:=]\s*(-?\d+(?:\.\d+)?)\s*(?:c\b|celsius|degrees)`)
	voltageReading     = regexp.MustCompile(`(?i)(?:^|[^a-z])voltage\s*[:=]\s*(-?\d+(?:\.\d+)?)\s*(?:volts?|v\b)`)
	currentReading     = regexp.MustCompile(`(?i)(?:^|[^a-z])current\s*[:=]\s*(-?\d+(?:\.\d+)?)\s*(?:amps?|a\b)`)
	sasAddress         = regexp.MustCompile(`(?i)^sas address\s*[:=]\s*(0x[0-9a-f]+)`)
	slotNumberLine     = regexp.MustCompile(`(?i)(?:device slot number|bay number)\s*[:=]\s*(\d+)`)
)

// parseJoinElements reads "sg_ses --join" output into elements.
//
// The join is the one call that carries the descriptor text, the element
// status and the Additional Element Status of every element at once, which
// is what makes a slot → SAS address → disk mapping possible without a
// separate page read per element (ROADMAP 5).
//
// The parser is a small state machine rather than a line regex because an
// element is a block: a header, then the flags and readings underneath it,
// until the next header. It accepts the three header shapes sg_ses has
// used; see elementTypeIn.
func parseJoinElements(out string) []sesElement {
	var elements []sesElement
	// The type of the block being read, for the shape that prints the type
	// once above its elements.
	currentType := ""
	currentTypeIndex := Optional[int64]{}
	var current *sesElement
	start := func(e sesElement) {
		elements = append(elements, e)
		current = &elements[len(elements)-1]
	}
	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if m := elementIndex.FindStringSubmatch(line); m != nil {
			typeIndex, err1 := strconv.ParseInt(m[2], 10, 64)
			element, err2 := strconv.ParseInt(m[3], 10, 64)
			if err1 != nil || err2 != nil {
				continue
			}
			// The header may name the type itself, or name none at all —
			// the shape that prints it once above the block, where the
			// block's type applies.
			name := elementTypeOf(m[4], currentType)
			start(sesElement{
				TypeIndex:  typeIndex,
				Element:    element,
				Index:      strconv.FormatInt(typeIndex, 10) + "," + strconv.FormatInt(element, 10),
				Descriptor: strings.TrimSpace(m[1]),
				Type:       name,
				Flags:      map[string]bool{},
			})
			continue
		}
		// A standalone type header: "Element type: Cooling, subenclosure
		// id: 0 [ti=3]". A header this package cannot name still ends the
		// previous block: leaving the previous type in place would file
		// its elements under the previous type index, which is another
		// element's address.
		if isTypeHeader(line) {
			currentType = elementTypeOf(line, "")
			currentTypeIndex = None[int64]()
			if m := typeIndexOnly.FindStringSubmatch(line); m != nil {
				if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
					currentTypeIndex = Some(n)
				}
			}
			current = nil
			continue
		}
		// An element listed under such a header, with no index of its own.
		if m := elementOrdinal.FindStringSubmatch(line); m != nil && currentTypeIndex.Present() {
			element, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				continue
			}
			typeIndex := currentTypeIndex.Or(0)
			start(sesElement{
				TypeIndex:  typeIndex,
				Element:    element,
				Index:      strconv.FormatInt(typeIndex, 10) + "," + m[1],
				Descriptor: strings.TrimSpace(descriptorAfter(line)),
				Type:       currentType,
				Flags:      map[string]bool{},
			})
			continue
		}
		if overallOrdinal.MatchString(line) && currentTypeIndex.Present() {
			typeIndex := currentTypeIndex.Or(0)
			start(sesElement{
				TypeIndex: typeIndex,
				Element:   -1,
				Index:     strconv.FormatInt(typeIndex, 10) + ",-1",
				Type:      currentType,
				Flags:     map[string]bool{},
			})
			continue
		}
		if current == nil {
			continue
		}
		absorb(current, line)
	}
	return elements
}

// descriptorAfter returns the text after the colon of an
// "Element 0 descriptor: SLOT 00" line, which some sg_ses versions print
// and others leave empty.
func descriptorAfter(line string) string {
	_, rest, ok := strings.Cut(line, ":")
	if !ok {
		return ""
	}
	return rest
}

// absorb reads one body line into the element it belongs to.
func absorb(e *sesElement, line string) {
	if !e.Status.Present() {
		if v, ok := tag(line, "status"); ok {
			e.Status = Some(v)
		}
	}
	for field := range strings.SplitSeq(line, ",") {
		m := flagPair.FindStringSubmatch(field)
		if m == nil {
			continue
		}
		if name := strings.TrimSpace(m[1]); name != "" {
			e.Flags[name] = m[2] == "1"
		}
	}
	if m := temperatureReading.FindStringSubmatch(line); m != nil && !e.Temperature.Present() {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			e.Temperature = Some(v)
		}
	}
	if m := voltageReading.FindStringSubmatch(line); m != nil && !e.Voltage.Present() {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			e.Voltage = Some(v)
		}
	}
	if m := currentReading.FindStringSubmatch(line); m != nil && !e.Current.Present() {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			e.Current = Some(v)
		}
	}
	if m := rpm.FindStringSubmatch(line); m != nil && !e.Speed.Present() {
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			e.Speed = Some(v)
		}
	}
	trimmed := strings.TrimSpace(line)
	if m := sasAddress.FindStringSubmatch(trimmed); m != nil {
		// A phy that is not connected reports the null address; publishing
		// it as an identity would map every empty bay to the same disk.
		if address := strings.ToLower(m[1]); !nullAddress(address) && !containsString(e.SASAddresses, address) {
			e.SASAddresses = append(e.SASAddresses, address)
		}
	}
	if m := slotNumberLine.FindStringSubmatch(line); m != nil && !e.SlotNumber.Present() {
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			e.SlotNumber = Some(v)
		}
	}
}

// nullAddress reports whether a SAS address is all zeros.
func nullAddress(address string) bool {
	return strings.Trim(strings.TrimPrefix(address, "0x"), "0") == ""
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// sesThreshold is one element's thresholds and the unit they are in.
type sesThreshold struct {
	HighCritical Optional[float64]
	HighWarning  Optional[float64]
	LowWarning   Optional[float64]
	LowCritical  Optional[float64]
	// Unit is UnitCelsius for a temperature sensor and
	// UnitPercentOfNominal for a voltage or current sensor, whose limits
	// the page states as an offset from the nominal value rather than as
	// volts or amps.
	Unit string
}

// present reports whether the enclosure declared any threshold at all.
func (t sesThreshold) present() bool {
	return t.HighCritical.Present() || t.HighWarning.Present() ||
		t.LowWarning.Present() || t.LowCritical.Present()
}

// SES element type codes of the elements that carry thresholds this package
// decodes (SES-3 table 70).
const (
	sesTypeTemperature = "temperature sensor"
	sesTypeVoltage     = "voltage sensor"
	sesTypeCurrent     = "current sensor"
)

// sesThresholdPage is the decoded Threshold In page.
type sesThresholdPage struct {
	// Generation is the generation code the page carries, in the spelling
	// the text pages use, so the pages of one pass can be compared.
	Generation string
	// Limits are keyed by the "[type,element]" index the other pages use;
	// the overall element of a type is kept under element -1.
	Limits map[string]sesThreshold
}

// parseThresholdPage decodes "sg_ses --page=th --raw": the Threshold In
// page as hex, from the generation code on.
//
// The page is decoded here rather than read from sg_ses's own text, for two
// reasons found in the sg3_utils sources. The text puts each element's
// limits on the lines under an "Element N descriptor:" header, in a layout
// that differs between versions. And from sg3_utils 1.48 sg_ses skips the
// element types that carry no thresholds without stepping over their
// descriptors, so on any shelf that lists its bays before its sensors —
// every shelf — the limits it prints for a sensor are bytes of some other
// element. The raw page is the same in every version: the generation code,
// then one four-byte descriptor for the overall element and for each
// element of every type, in Configuration page order (SES-3 6.1.8).
//
// The configuration is what gives the descriptors their meaning, so the
// page is decoded only when it carries exactly as many descriptors as the
// configuration declares and the same generation code. Anything else is an
// error with both numbers in it and no thresholds: a limit attached to the
// wrong element is a wrong number, not a missing one.
//
// A field of 00h is "not supported" for every sensor type (SES-3 7.3.x) and
// stays absent. Nothing here is written back: changing a threshold is a
// control page, which is 1.4 (ROADMAP 7).
func parseThresholdPage(out string, cfg sesConfig) (sesThresholdPage, error) {
	page := sesThresholdPage{Limits: map[string]sesThreshold{}}
	raw, err := parseHexDump(out)
	if err != nil {
		return page, err
	}
	if len(raw) < 4 {
		return page, fmt.Errorf("the page is %d bytes, shorter than its generation code", len(raw))
	}
	page.Generation = fmt.Sprintf("0x%x", binary.BigEndian.Uint32(raw[:4]))
	if want, ok := cfg.Generation.Get(); ok && !strings.EqualFold(strings.TrimSpace(want), page.Generation) {
		return page, fmt.Errorf("the page has generation code %s and the configuration page %s; "+
			"the descriptors are not decoded against a configuration they do not belong to", page.Generation, want)
	}
	descriptors := raw[4:]
	if len(descriptors)%4 != 0 {
		return page, fmt.Errorf("the descriptor list is %d bytes, not a whole number of 4-byte descriptors", len(descriptors))
	}
	if len(cfg.Types) == 0 {
		return page, errors.New("the configuration page declared no element types to decode the page against")
	}
	declared := 0
	for _, t := range cfg.Types {
		n, ok := t.Possible.Get()
		if !ok {
			return page, fmt.Errorf("the configuration page declared no element count for type %d (%s)", t.TypeIndex, t.Type)
		}
		declared += 1 + int(n)
	}
	if got := len(descriptors) / 4; got != declared {
		return page, fmt.Errorf("the page carries %d descriptors and the configuration declares %d elements "+
			"(each type's overall element included)", got, declared)
	}
	offset := 0
	for _, t := range cfg.Types {
		n := int(t.Possible.Or(0))
		for element := -1; element < n; element++ {
			d := descriptors[offset : offset+4]
			offset += 4
			limits, ok := decodeThreshold(t.Type, d)
			if !ok {
				continue
			}
			page.Limits[strconv.FormatInt(t.TypeIndex, 10)+","+strconv.Itoa(element)] = limits
		}
	}
	return page, nil
}

// decodeThreshold reads one threshold status descriptor of an element type.
// It is false for a type without thresholds this package knows the unit of
// and for a descriptor that declares none.
func decodeThreshold(elementType string, d []byte) (sesThreshold, bool) {
	var t sesThreshold
	field := func(b byte, scale func(byte) float64) Optional[float64] {
		if b == 0 {
			return None[float64]()
		}
		return Some(scale(b))
	}
	// A temperature is offset by 20 so that one byte covers -19 to 235 C.
	celsius := func(b byte) float64 { return float64(int(b) - 20) }
	// Voltage and current limits are in units of 0.5 % of nominal.
	percent := func(b byte) float64 { return float64(b) / 2 }
	switch elementType {
	case sesTypeTemperature:
		t = sesThreshold{
			HighCritical: field(d[0], celsius), HighWarning: field(d[1], celsius),
			LowWarning: field(d[2], celsius), LowCritical: field(d[3], celsius),
			Unit: UnitCelsius,
		}
	case sesTypeVoltage:
		// High limits are above nominal and low limits below it.
		t = sesThreshold{
			HighCritical: field(d[0], percent), HighWarning: field(d[1], percent),
			LowWarning: field(d[2], percent), LowCritical: field(d[3], percent),
			Unit: UnitPercentOfNominal,
		}
	case sesTypeCurrent:
		// A current sensor has high limits only; the low fields are
		// reserved.
		t = sesThreshold{
			HighCritical: field(d[0], percent), HighWarning: field(d[1], percent),
			Unit: UnitPercentOfNominal,
		}
	default:
		return t, false
	}
	return t, t.present()
}

// parseHexDump reads the hex sg_ses prints for --raw: two hex digits per
// byte, sixteen to a line, with no offsets and no ASCII column. A token that
// is not a byte means the output is something else, and it is refused
// rather than skipped, because a skipped byte shifts every descriptor after
// it.
func parseHexDump(out string) ([]byte, error) {
	var raw []byte
	for _, field := range strings.Fields(out) {
		if len(field) != 2 {
			return nil, fmt.Errorf("%q is not a hex byte; the output is not a raw page", field)
		}
		b, err := strconv.ParseUint(field, 16, 8)
		if err != nil {
			return nil, fmt.Errorf("%q is not a hex byte; the output is not a raw page", field)
		}
		raw = append(raw, byte(b))
	}
	if len(raw) == 0 {
		return nil, errors.New("the page is empty")
	}
	return raw, nil
}
