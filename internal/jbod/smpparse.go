// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"regexp"
	"strconv"
	"strings"
)

// This file parses the output of smp_utils, the way sespage.go parses the
// output of sg_ses: pure functions of the text they are given, so the SMP
// half of the phy report can be exercised from fixtures without an
// expander (ROADMAP 10).
//
// The same rule applies as everywhere else here: a field this parser does
// not recognise stays absent. An unparsed line is survivable; an unparsed
// line that becomes a zero error counter is a clean link that is not.

// intTagAny returns the first of several spellings of a numeric field that
// the line carries.
//
// The spellings are not interchangeable prefixes: tag requires the
// separator to follow the name, so "loss of dword sync" does not match
// "loss of dword synchronization count". Both have been printed by
// smp_utils, so both are listed.
func intTagAny(line string, names ...string) (int64, bool) {
	for _, name := range names {
		if v, ok := intTag(line, name); ok {
			return v, true
		}
	}
	return 0, false
}

// parseSMPGeneral reads the phy count out of "smp_rep_general" output,
// which is the expander's own answer to how many phys to ask about.
func parseSMPGeneral(out string) Optional[int64] {
	for line := range strings.Lines(out) {
		if n, ok := intTagAny(line, "number of phys", "number of phy"); ok {
			return Some(n)
		}
	}
	return None[int64]()
}

// The three shapes "smp_discover --multiple" prints, one line per phy:
//
//	phy   0: inaccessible (phy vacant)
//	phy  47:U:disabled
//	phy  24:U:attached:[5000ccab05629d3f:60 exp i(SMP) t(SMP)]  12 Gbps
//
// They are matched in layers rather than by one pattern, because only the
// phy number is common to all three. Reading the line as "phy number, then
// whatever the expander had to say" is also what keeps a fourth shape from
// costing the phy: it keeps its number and its error counters, and the
// words go into Detail (ROADMAP 6, hardware run).
var (
	smpPhyLine = regexp.MustCompile(`(?i)^\s*phy\s+(\d+)\s*:\s*(.*)$`)
	// smpRouting is the single routing-attribute letter, when the line has
	// one. It must be a single letter followed by a colon, so the "i" of
	// "inaccessible" cannot be read as one.
	smpRouting = regexp.MustCompile(`^([A-Za-z])\s*:\s*(.*)$`)
	// smpAttached is the far end: its address, its phy identifier, what it
	// announced, and then the negotiated rate.
	smpAttached = regexp.MustCompile(`(?i)^attached\s*:\s*\[\s*([0-9a-fA-F]+)\s*:\s*(\d+)([^\]]*)\]\s*(.*)$`)
)

// smpPhyStateOf reads the state out of the words a line carries in place of
// an attached device.
//
// "Vacant" is the one that matters: the expander declares 49 phys and says
// 24 of them are not there, and that is an answer, not a gap. It is also
// why those phys have no error log to ask for.
func smpPhyStateOf(text string) PHYState {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "vacant"):
		return PHYStateVacant
	case strings.Contains(lower, "disabled"):
		return PHYStateDisabled
	case strings.Contains(lower, "failed"), strings.Contains(lower, "reset problem"):
		return PHYStateFailed
	case strings.Contains(lower, "spin"):
		return PHYStateSpinupHold
	default:
		return PHYStateUnknown
	}
}

// parseSMPDiscoverList reads "smp_discover --multiple" output into phys.
//
// It is deliberately forgiving about everything but the phy number: this
// one-line format is a convenience of smp_utils rather than a stable
// interface, and losing the attached address to a changed spelling must
// not also lose the phy, which is what the error counters are keyed by.
func parseSMPDiscoverList(out, source string) []SMPPhy {
	var phys []SMPPhy
	seen := map[int64]bool{}
	for line := range strings.Lines(out) {
		// The line separator is stripped first: the patterns anchor at the
		// end of the text, so a trailing newline would leave every line
		// that carries a rate unmatched and only the bare ones parsed.
		m := smpPhyLine.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
		if m == nil {
			continue
		}
		identifier, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || seen[identifier] {
			continue
		}
		seen[identifier] = true
		phy := SMPPhy{Identifier: identifier, State: PHYStateUnknown, Source: source}
		rest := strings.TrimSpace(m[2])
		// The routing letter is kept exactly as smp_discover prints it.
		// Three of them are documented — D direct, S subtractive, T table
		// — and a real WD expander printed a fourth, "U", for 146 of its
		// 148 phys. Expanding the three and passing the fourth through put
		// two vocabularies in one column, and the one that mattered was
		// the one this package cannot name.
		if routing := smpRouting.FindStringSubmatch(rest); routing != nil {
			phy.Routing = Some(strings.ToUpper(routing[1]))
			rest = strings.TrimSpace(routing[2])
		}
		attached := smpAttached.FindStringSubmatch(rest)
		if attached == nil {
			// No far end: the words are the expander's answer about this
			// phy, and they are kept as they came.
			phy.State = smpPhyStateOf(rest)
			if rest != "" {
				phy.Detail = Some(rest)
			}
			phys = append(phys, phy)
			continue
		}
		if address, ok := smpAddress(attached[1]); ok {
			phy.AttachedAddress = Some(address)
			// The attached phy identifier only means something next to an
			// address: printed on its own for an empty connector it would
			// read as phy 0 of some device.
			if n, err := strconv.ParseInt(strings.TrimSpace(attached[2]), 10, 64); err == nil {
				phy.AttachedPhy = Some(n)
			}
		}
		if protocols := strings.TrimSpace(attached[3]); protocols != "" {
			phy.AttachedProtocols = Some(protocols)
		}
		// The text after the bracket starts with the negotiated rate and
		// may carry more fields after it: this expander appends the zone
		// group ("12 Gbps  ZG:14"). Fields are separated by a run of two
		// spaces, the same way sg_ses separates them, so the rate ends
		// there. Without this the rate reads "12 Gbps  ZG:14".
		rate := strings.TrimSpace(attached[4])
		if loc := twoSpaces.FindStringIndex(rate); loc != nil {
			rate = strings.TrimSpace(rate[:loc[0]])
		}
		phy.Negotiated = parseLinkRate(rate, rate != "")
		phy.State = phyState(phy.Negotiated, None[bool]())
		phys = append(phys, phy)
	}
	return phys
}

// smpAddress normalises a SAS address smp_utils printed without its 0x
// prefix, and rejects the null address for the same reason readSASAddress
// does: it is not an identity.
func smpAddress(raw string) (string, bool) {
	address := strings.ToLower(strings.TrimSpace(raw))
	if address == "" || nullAddress(address) {
		return "", false
	}
	if !strings.HasPrefix(address, "0x") {
		address = "0x" + address
	}
	return address, true
}

// parseSMPPhyErrorLog reads "smp_rep_phy_err_log" output. It returns the
// counters and the phy identifier the response names, so the caller can
// check that the answer is about the phy it asked for.
func parseSMPPhyErrorLog(out string) (ErrorCounters, Optional[int64]) {
	var counters ErrorCounters
	phy := None[int64]()
	for line := range strings.Lines(out) {
		if n, ok := intTag(line, "phy identifier"); ok && !phy.Present() {
			phy = Some(n)
		}
		if n, ok := intTagAny(line, "invalid dword count", "invalid dwords"); ok && !counters.InvalidDword.Present() {
			counters.InvalidDword = Some(n)
		}
		if n, ok := intTagAny(line, "running disparity error count", "running disparity errors"); ok &&
			!counters.RunningDisparityError.Present() {
			counters.RunningDisparityError = Some(n)
		}
		if n, ok := intTagAny(line,
			"loss of dword synchronization count", "loss of dword sync count",
			"loss of dword synchronization", "loss of dword sync"); ok && !counters.LossOfDwordSync.Present() {
			counters.LossOfDwordSync = Some(n)
		}
		if n, ok := intTagAny(line, "phy reset problem count", "phy reset problems"); ok &&
			!counters.PhyResetProblem.Present() {
			counters.PhyResetProblem = Some(n)
		}
	}
	return counters, phy
}
