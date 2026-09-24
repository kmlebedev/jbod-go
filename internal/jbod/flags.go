// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"slices"
	"strings"
)

// This file names the one-bit fields of the SES element status under stable
// names (ROADMAP 5).
//
// Component.Flags keeps the fields as sg_ses prints them, and that spelling
// is not an interface: the same bit is "Fault reqstd" for an array device
// slot and "Fault requested" for a device slot, "App client bypass A" in one
// and "App client bypassed A" in the other, and a later sg_ses may spell
// them differently again. A metric label or an alert rule needs one name per
// bit, so the spellings of sg3-utils 1.44 to 1.48 are mapped here onto the
// field names of SES-3, in snake case. The two over-temperature bits of a
// power supply and the ones of a temperature sensor share their names, so
// one rule covers both.
//
// A field this table does not know is not published under any name. That
// is also what keeps the numbers out: sg_ses prints "Actual speed=0 rpm",
// "Time until power cycle=0" and "Size multiplier=1" in the same "Name=N"
// shape, and a zero or a one among them is not a bit.
var flagNames = map[string]string{
	// Common to every element type (byte 0).
	"predicted failure": "predicted_failure",
	"disabled":          "disabled",
	"swap":              "swap",

	// Array device slot and device slot.
	"ok":                    "ok",
	"reserved device":       "reserved_device",
	"hot spare":             "hot_spare",
	"cons check":            "cons_check",
	"in crit array":         "in_crit_array",
	"in failed array":       "in_failed_array",
	"rebuild/remap":         "rebuild_remap",
	"r/r abort":             "rr_abort",
	"app client bypass a":   "app_client_bypassed_a",
	"app client bypassed a": "app_client_bypassed_a",
	"app client bypass b":   "app_client_bypassed_b",
	"app client bypassed b": "app_client_bypassed_b",
	"do not remove":         "do_not_remove",
	"enc bypass a":          "enclosure_bypassed_a",
	"enc bypassed a":        "enclosure_bypassed_a",
	"enc bypass b":          "enclosure_bypassed_b",
	"enc bypassed b":        "enclosure_bypassed_b",
	"enable bypass a":       "enable_bypass_a",
	"enable bypass b":       "enable_bypass_b",
	"bypass a enabled":      "bypass_a_enabled",
	"bypass b enabled":      "bypass_b_enabled",
	"ready to insert":       "ready_to_insert",
	"rmv":                   "rmv",
	"ident":                 "ident",
	"report":                "report",
	"fault sensed":          "fault_sensed",
	"fault reqstd":          "fault_requested",
	"fault requested":       "fault_requested",
	"device off":            "device_off",
	"bypassed a":            "bypassed_a",
	"bypassed b":            "bypassed_b",
	"dev bypassed a":        "device_bypassed_a",
	"device bypassed a":     "device_bypassed_a",
	"dev bypassed b":        "device_bypassed_b",
	"device bypassed b":     "device_bypassed_b",

	// Power supply and cooling.
	"dc overvoltage":   "dc_overvoltage",
	"dc undervoltage":  "dc_undervoltage",
	"dc overcurrent":   "dc_overcurrent",
	"hot swap":         "hot_swap",
	"fail":             "fail",
	"requested on":     "requested_on",
	"off":              "off",
	"overtmp fail":     "overtemp_failure",
	"temperature warn": "overtemp_warning",
	"ac fail":          "ac_fail",
	"dc fail":          "dc_fail",

	// Temperature sensor.
	"ot failure": "overtemp_failure",
	"ot warning": "overtemp_warning",
	"ut failure": "undertemp_failure",
	"ut warning": "undertemp_warning",

	// Voltage and current sensors.
	"warn over":  "warn_over",
	"warn under": "warn_under",
	"crit over":  "crit_over",
	"crit under": "crit_under",

	// SAS connector.
	"mated": "mated",
	"oc":    "overcurrent",

	// Enclosure.
	"failure indication": "failure_indication",
	"warning indication": "warning_indication",
	"failure requested":  "failure_requested",
	"warning requested":  "warning_requested",

	// Door and audible alarm.
	"open":         "open",
	"unlock":       "unlock",
	"request mute": "request_mute",
	"mute":         "mute",
	"remind":       "remind",
	"info":         "info",
	"non-crit":     "non_critical",
	"crit":         "critical",
	"unrecov":      "unrecoverable",

	// Ports.
	"loss of link": "loss_of_link",
	"xmit fail":    "xmit_fail",
	"enabled":      "enabled",

	// Uninterruptible power supply.
	"ac low":    "ac_low",
	"ac high":   "ac_high",
	"ac qual":   "ac_qual",
	"ups fail":  "ups_fail",
	"warn":      "warn",
	"intf fail": "interface_fail",
	"batt fail": "battery_fail",
	"bpf":       "bpf",
}

// capabilityFlags are bits that say what an element can do rather than what
// state it is in. "Hot swap" on a power supply, a fan or an I/O module means
// the element may be removed without halting the shelf: it is set on every
// such element of a WD H4060-J, all the time, and it would be the one bit
// published for them. They stay in Component.Flags and are not a status.
var capabilityFlags = map[string]bool{"hot_swap": true}

// FlagName returns the stable name of a status field as sg_ses prints it,
// and false for a field that is not a known one-bit field.
func FlagName(printed string) (string, bool) {
	name, ok := flagNames[strings.Join(strings.Fields(strings.ToLower(printed)), " ")]
	return name, ok
}

// ComponentFlag is one status bit of an element under its stable name.
type ComponentFlag struct {
	Name string
	Set  bool
}

// StatusFlags returns the element's known status bits under their stable
// names, sorted by name. Fields FlagName does not know are left out, and so
// are the capability bits (see capabilityFlags).
//
// An element the reporting module has no access to returns none. "No access
// allowed" is what an I/O module answers for the half of a two-module shelf
// it does not own, and the bits it prints next to that code describe
// nothing: the owning module reports the element, and its answer is the one
// with meaning. Publishing both would put a zero next to the real value.
func (c Component) StatusFlags() []ComponentFlag {
	if c.NoAccess() {
		return nil
	}
	// The printed names are walked in order, so that two spellings of one
	// bit, should an element ever carry both, resolve the same way on
	// every scrape.
	printed := make([]string, 0, len(c.Flags))
	for name := range c.Flags {
		printed = append(printed, name)
	}
	slices.Sort(printed)
	var flags []ComponentFlag
	seen := map[string]bool{}
	for _, p := range printed {
		name, ok := FlagName(p)
		if !ok || seen[name] || capabilityFlags[name] {
			continue
		}
		seen[name] = true
		flags = append(flags, ComponentFlag{Name: name, Set: c.Flags[p]})
	}
	slices.SortFunc(flags, func(a, b ComponentFlag) int { return strings.Compare(a.Name, b.Name) })
	return flags
}

// NoAccess reports an element whose status the reporting module is not
// allowed to read (SES element status code 8).
func (c Component) NoAccess() bool {
	return strings.EqualFold(strings.Join(strings.Fields(c.Status.Or("")), " "), "no access allowed")
}
