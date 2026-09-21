// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"encoding/json"
	"io"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// The --json documents. They are declared here rather than assembled ad hoc
// so the shape of the machine-readable output is one reviewable thing: a
// stable set of top-level keys, and every reading rendered as its value or
// as null. A field that is null means the hardware did not report it, which
// the tables spell as N/A and a consumer must not read as zero (ROADMAP 3).

// listDocument is what "jbod list --json" prints. Sections that were not
// asked for are omitted entirely, so a consumer can tell "not requested"
// from "requested and empty", which is an empty array.
// The sections are pointers because omitempty cannot tell a nil slice from
// an empty one: a shelf that really has no fans must render as [] and only
// a section nobody asked for may be missing.
type listDocument struct {
	Enclosures *[]jbod.Enclosure `json:"enclosures,omitempty"`
	Slots      *[]jbod.Slot      `json:"slots,omitempty"`
	Disks      *[]jbod.Disk      `json:"disks,omitempty"`
	Fans       *[]jbod.Fan       `json:"fans,omitempty"`
	Components *[]jbod.Component `json:"components,omitempty"`
}

// healthDocument is what "jbod health --json" prints: one report per shelf,
// each carrying the hardware verdict, the component roll-up and the
// completeness of the poll as three separate things (ROADMAP 5).
type healthDocument struct {
	Enclosures []jbod.EnclosureStatus `json:"enclosures"`
}

// sensorsDocument is what "jbod sensors --json" prints.
type sensorsDocument struct {
	Enclosures []sensorSection `json:"enclosures"`
}

// capabilitiesDocument is what "jbod capabilities --json" prints.
type capabilitiesDocument struct {
	Enclosures []jbod.EnclosureCapabilities `json:"enclosures"`
}

// ledDocument is what "jbod led --json" prints. It carries the outcome of
// every write that was attempted, including the ones that were accepted but
// not confirmed.
type ledDocument struct {
	Results []jbod.LEDResult `json:"results"`
}

// writeJSON prints v indented, with HTML escaping off so a device path or a
// vendor string survives unchanged.
func writeJSON(out io.Writer, v any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(v)
}

// emptyToSlice replaces a nil slice with an empty one, so a requested but
// empty section renders as [] and not as null.
func emptyToSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// section marks a slice as requested, so it is rendered even when empty.
func section[T any](s []T) *[]T {
	s = emptyToSlice(s)
	return &s
}
