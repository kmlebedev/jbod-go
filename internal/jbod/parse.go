// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// This file holds the parsers of untrusted hardware output: the text of
// lsscsi, sg_map, sg_ses, sginfo and scsi_temperature, and the binary VPD
// page 0x80. They are pure functions of their input so they can be tested
// and fuzzed without a shelf, an expander or a sysfs tree (D3).

// field returns the value of a "Key: value" line, if the output has one.
func field(out, key string) (string, bool) {
	for line := range strings.Lines(out) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// enclosureRef is one enclosure as lsscsi reports it, before its identity is
// read with sg_inq.
type enclosureRef struct {
	Slot   string
	Device string
}

// parseLsscsi picks the enclosures out of "lsscsi -g" output. Lines that look
// like an enclosure but cannot be used are returned as errors, so one bad
// line does not hide the working shelves.
func parseLsscsi(out string) ([]enclosureRef, []error) {
	var refs []enclosureRef
	var errs []error
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[1], "enclosu") {
			continue
		}
		// The generic device is the last column of "lsscsi -g". Taking the
		// first /dev/ token instead would pick up a block device if the
		// enclosure ever reported one, so the column is required to look
		// like /dev/sgN (A7).
		device := f[len(f)-1]
		if !strings.HasPrefix(device, "/dev/sg") {
			errs = append(errs, fmt.Errorf("enclosure has no generic device: %s", strings.TrimSpace(line)))
			continue
		}
		// The slot becomes a path element under the sysfs root, so anything
		// that could escape it is rejected rather than joined.
		slot := strings.Trim(f[0], "[]")
		if slot == "" || strings.ContainsAny(slot, "/\\") || slot == "." || slot == ".." {
			errs = append(errs, fmt.Errorf("invalid enclosure slot %q", slot))
			continue
		}
		refs = append(refs, enclosureRef{Slot: slot, Device: device})
	}
	return refs, errs
}

// parseSgMap maps a generic device to its block device, from "sg_map" output.
func parseSgMap(out string) map[string]string {
	mapping := map[string]string{}
	for line := range strings.Lines(out) {
		if f := strings.Fields(line); len(f) > 1 {
			mapping[f[0]] = f[1]
		}
	}
	return mapping
}

var number = regexp.MustCompile(`-?\d+`)

// parseTemperature extracts the current temperature in degrees Celsius from
// scsi_temperature output.
//
// The separator is not fixed. scsi_temperature is a shell wrapper around
// "sg_logs --temperature", which prints
//
//	Current temperature = 33 C
//
// with an equals sign, while other builds and smartctl print a colon
// ("Current Drive Temperature: 33 C"). Requiring a colon is what made a
// 60-disk shelf report ERR for every drive on a WD H4060-J, so both
// separators are accepted.
//
// A separator is still required. Taking the first number out of any line
// mentioning a current temperature would read "Current temperature sensor 2
// unavailable" as two degrees, and a wrong reading is worse than none.
func parseTemperature(out string) (int64, bool) {
	for line := range strings.Lines(out) {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "current") || !strings.Contains(lower, "temperature") {
			continue
		}
		value, ok := temperatureValue(line)
		if !ok {
			continue
		}
		n := number.FindString(value)
		if n == "" {
			continue
		}
		if v, err := strconv.ParseInt(n, 10, 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

// temperatureValue returns the part of a temperature line that holds the
// reading, so a number inside the label ("Current temperature 1 = 33 C")
// cannot be mistaken for it. It returns false when the line carries no
// separator and therefore no value this parser will trust.
func temperatureValue(line string) (string, bool) {
	i := strings.IndexAny(line, ":=")
	if i < 0 {
		return "", false
	}
	return line[i+1:], true
}

// parseVPD80 decodes the unit serial number from VPD page 0x80, which is
// binary: a four-byte header carrying the page code and a big-endian length,
// then the serial itself.
func parseVPD80(b []byte) (string, bool) {
	if len(b) < 4 || b[1] != 0x80 {
		return "", false
	}
	n := int(binary.BigEndian.Uint16(b[2:4]))
	if n > len(b)-4 {
		return "", false
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(b[4:4+n]), "�")), true
}

var fanLine = regexp.MustCompile(`(.*?)\[(-?\d+,-?\d+)\].*Cooling`)
var rpm = regexp.MustCompile(`(?i)(\d+)\s*rpm`)

// fanRef is one cooling element of an enclosure, before its speed is known.
type fanRef struct {
	Description string
	Index       string
	// Overall marks the SES overall element of the cooling type, which
	// summarises the fans instead of being one.
	Overall bool
}

// parseFanElements picks the cooling elements out of "sg_ses -j -ff" output.
//
// An element index of -1 is SES's overall element for the type: a summary of
// every fan of the enclosure, not a fan. A WD H4060-J reports it as
// "[3,-1] ... Fan stopped" at 0 RPM, which the listing used to print as a
// dead fan and the exporter used to publish as a zero-RPM series — a false
// alarm on a shelf whose fans are all running. Overall elements are counted
// but not listed as devices.
func parseFanElements(out string) []fanRef {
	var refs []fanRef
	for line := range strings.Lines(out) {
		m := fanLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ref := fanRef{Description: strings.TrimSpace(m[1]), Index: m[2]}
		if _, element, ok := strings.Cut(ref.Index, ","); ok && strings.TrimSpace(element) == "-1" {
			ref.Overall = true
		}
		refs = append(refs, ref)
	}
	return refs
}

// individual returns the elements that are real fans.
func individual(refs []fanRef) []fanRef {
	kept := refs[:0:0]
	for _, ref := range refs {
		if !ref.Overall {
			kept = append(kept, ref)
		}
	}
	return kept
}

// parseFanSpeed reads the speed of one element from "sg_ses --index=" output,
// as in "speed code: 2, Actual speed: 1200 rpm, low speed". The condition
// after the speed is optional: some enclosures print only two fields.
func parseFanSpeed(out string) (int64, Optional[string], bool) {
	match := rpm.FindStringSubmatch(out)
	if match == nil {
		return 0, None[string](), false
	}
	speed, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		// The pattern only matches digits, so this is an overflow.
		return 0, None[string](), false
	}
	condition := None[string]()
	for line := range strings.Lines(out) {
		if !rpm.MatchString(line) {
			continue
		}
		if parts := strings.SplitN(line, ",", 3); len(parts) == 3 {
			condition = Some(strings.TrimSpace(parts[2]))
		}
	}
	return speed, condition, true
}
