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

// smpDiscoverLine matches one line of "smp_discover --multiple" output:
//
//	phy   0:D:attached:[5000cca25de1c2ed:00  t(SSP)]  12 Gbps
//	phy  12:U:attached:[0000000000000000:00]
//
// The trailing text is the negotiated logical link rate as smp_utils
// spells it, which is sometimes a rate and sometimes a reason there is
// none ("disabled", "phy enabled; unknown rate").
var smpDiscoverLine = regexp.MustCompile(
	`(?i)^\s*phy\s+(\d+)\s*:\s*([A-Za-z])\s*:\s*attached\s*:\s*\[\s*([0-9a-fA-F]+)\s*:\s*(\d+)([^\]]*)\]\s*(.*)$`)

// routingAttributes are the routing attribute letters smp_discover prints.
// A letter that is not one of them keeps its raw form: inventing a meaning
// for it would be a claim about the topology nobody made.
var routingAttributes = map[string]string{
	"D": "direct",
	"S": "subtractive",
	"T": "table",
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
		// The line separator is stripped first: the pattern anchors at the
		// end of the text, so a trailing newline would leave every line
		// that carries a rate unmatched and only the bare ones parsed.
		m := smpDiscoverLine.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
		if m == nil {
			continue
		}
		identifier, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || seen[identifier] {
			continue
		}
		seen[identifier] = true
		phy := SMPPhy{Identifier: identifier, Source: source}
		if routing := strings.ToUpper(strings.TrimSpace(m[2])); routing != "" {
			phy.Routing = Some(routingAttributes[routing])
			if routingAttributes[routing] == "" {
				phy.Routing = Some(routing)
			}
		}
		if address, ok := smpAddress(m[3]); ok {
			phy.AttachedAddress = Some(address)
			// The attached phy identifier only means something next to an
			// address: printed on its own for an empty connector it would
			// read as phy 0 of some device.
			if n, err := strconv.ParseInt(strings.TrimSpace(m[4]), 10, 64); err == nil {
				phy.AttachedPhy = Some(n)
			}
		}
		if protocols := strings.TrimSpace(m[5]); protocols != "" {
			phy.AttachedProtocols = Some(protocols)
		}
		rate := strings.TrimSpace(m[6])
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
