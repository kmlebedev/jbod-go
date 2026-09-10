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
		device := ""
		for _, v := range f[2:] {
			if strings.HasPrefix(v, "/dev/") {
				device = v
				break
			}
		}
		if device == "" {
			errs = append(errs, fmt.Errorf("enclosure has no device: %s", strings.TrimSpace(line)))
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
func parseTemperature(out string) (int64, bool) {
	for line := range strings.Lines(out) {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "current") || !strings.Contains(lower, "temperature") {
			continue
		}
		_, value, ok := strings.Cut(line, ":")
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
}

// parseFanElements picks the cooling elements out of "sg_ses -j -ff" output.
func parseFanElements(out string) []fanRef {
	var refs []fanRef
	for line := range strings.Lines(out) {
		m := fanLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		refs = append(refs, fanRef{Description: strings.TrimSpace(m[1]), Index: m[2]})
	}
	return refs
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
