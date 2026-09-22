// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// This file asks the expander itself, over SMP, what the sysfs transport
// class cannot answer: the far end of every link and the error counters the
// expander keeps for its own phys (ROADMAP 6).
//
// Three rules shape it:
//
//   - smp_utils is not a dependency of this tool. It is needed by this one
//     capability and nothing else, so a machine without it loses the SMP
//     half of the phy report and keeps everything else (ROADMAP 3).
//
//   - Nothing here clears a counter. smp_rep_phy_err_log takes a --zero
//     option that resets the counters as a side effect of reading them, and
//     a diagnostic that destroys the history of a marginal cable is worse
//     than no diagnostic. The option is never passed.
//
//   - SMP is opt-in. One request per phy through one SMP processor is fine
//     for a command an operator typed and wrong for a scrape that repeats
//     every fifteen seconds.

// DefaultBSGDir is where Linux exposes the bsg nodes SMP requests are
// addressed to.
const DefaultBSGDir = "/dev/bsg"

// The smp_utils binaries this package runs. They are deliberately absent
// from the required command list: Preflight must not fail on a machine that
// has no expander.
const (
	smpReportGeneral = "smp_rep_general"
	smpDiscover      = "smp_discover"
	smpPhyErrorLog   = "smp_rep_phy_err_log"
)

// smpPhyLimit bounds how many phys are asked about, whatever the expander
// reports. A garbled phy count must not turn into thousands of SMP
// requests through one expander.
const smpPhyLimit = 256

// SMPStatus is whether SMP was used and how it went, kept apart from what
// SMP reported (ROADMAP 3).
type SMPStatus struct {
	// Requested is whether SMP was asked for at all. Without it every
	// other field of this struct is meaningless rather than negative.
	Requested bool `json:"requested"`
	// Available is whether smp_utils and an SMP target were both there.
	Available bool `json:"available"`
	// Command is the request that was run, so a number can be checked
	// against the hardware by hand.
	Command string `json:"command,omitempty"`
	// OK is whether the expander answered it.
	OK bool `json:"ok"`
	// Phys counts the phys whose error log was read.
	Phys int `json:"phys_read"`
	// Err is why SMP produced nothing, which is not a statement about the
	// links.
	Err Optional[string] `json:"error"`
}

// SMPPhy is one expander phy as SMP describes it.
type SMPPhy struct {
	// Identifier is the phy number within the expander, which is how SMP
	// addresses it.
	Identifier int64 `json:"phy_identifier"`
	// Routing is the routing attribute smp_discover reports: direct,
	// subtractive or table. A spelling this package does not know keeps
	// its raw form rather than being folded into one of the three.
	Routing Optional[string] `json:"routing_attribute"`
	State   PHYState         `json:"state"`
	// Negotiated is the logical link rate this phy came up at.
	Negotiated LinkRate `json:"negotiated_link_rate"`
	// AttachedAddress is the SAS address on the other side of the link,
	// which is the one thing sysfs cannot tell us about an expander phy.
	AttachedAddress Optional[string] `json:"attached_sas_address"`
	// AttachedPhy is the phy identifier at that far end.
	AttachedPhy Optional[int64] `json:"attached_phy_identifier"`
	// AttachedProtocols is what the far end announced, as smp_discover
	// prints it ("t(SSP)", "i(SSP+STP+SMP)").
	AttachedProtocols Optional[string] `json:"attached_protocols"`
	// Counters are the expander's own error counters for this phy.
	Counters ErrorCounters `json:"error_counters"`
	// Source is the command this phy was described by.
	Source string `json:"source"`
}

// addSMP fills in the SMP half of the given reports.
//
// The work is done in two passes because the second depends on the first:
// an expander has to say how many phys it has before its phys can be asked
// for their error logs. Expanders run in parallel because they are separate
// SMP processors; the per-phy requests are flattened into one bounded pass
// so a rack of expanders does not multiply the concurrency limit.
func (c *Client) addSMP(ctx context.Context, reports []SASReport, p *problems) {
	var targets []*Expander
	for i := range reports {
		reports[i].Collection.SMP.Requested = true
		for j := range reports[i].Expanders {
			targets = append(targets, &reports[i].Expanders[j])
		}
	}
	forEach(ctx, c.concurrency, len(targets), func(i int) { c.smpExpander(ctx, targets[i], p) })
	type job struct {
		expander *Expander
		phy      *SMPPhy
	}
	var jobs []job
	for _, expander := range targets {
		if !expander.SMP.OK {
			continue
		}
		for k := range expander.Phys {
			jobs = append(jobs, job{expander: expander, phy: &expander.Phys[k]})
		}
	}
	forEach(ctx, c.concurrency, len(jobs), func(i int) {
		c.smpPhyErrors(ctx, *jobs[i].expander, jobs[i].phy, p)
	})
	// The phys are counted here rather than in the pass above: the pass
	// runs one goroutine per phy and several of them share an expander,
	// so incrementing a field of it there would be a data race.
	for _, expander := range targets {
		for _, phy := range expander.Phys {
			if phy.Counters.Present() {
				expander.SMP.Phys++
			}
		}
	}
	for i := range reports {
		reports[i].Collection.SMP = summarizeSMP(reports[i].Expanders)
	}
}

// summarizeSMP rolls the per-expander outcomes up for one host.
func summarizeSMP(expanders []Expander) SMPStatus {
	status := SMPStatus{Requested: true}
	if len(expanders) == 0 {
		status.Err = Some("no SAS expander is registered for this host, so there is nothing to ask over SMP; " +
			"a directly attached shelf has no expander and this is not a failure")
		return status
	}
	var failures []string
	for _, expander := range expanders {
		if expander.SMP.Available {
			status.Available = true
		}
		if expander.SMP.OK {
			status.OK = true
			status.Phys += expander.SMP.Phys
			continue
		}
		if err, ok := expander.SMP.Err.Get(); ok {
			failures = append(failures, expander.Name+": "+err)
		}
	}
	if len(failures) > 0 {
		status.Err = Some(strings.Join(failures, "; "))
	}
	return status
}

// smpExpander asks one expander how many phys it has and what is attached
// to each of them.
func (c *Client) smpExpander(ctx context.Context, expander *Expander, p *problems) {
	expander.SMP.Requested = true
	device, ok := expander.SMPDevice.Get()
	if !ok {
		// No bsg node means no way to address SMP at all. It is not a
		// statement about the expander, and it is not an error either: a
		// kernel built without bsg simply offers no SMP path.
		expander.SMP.Err = Some("the kernel exposes no bsg node for " + expander.Name +
			", so SMP requests cannot be addressed to it")
		return
	}
	expander.SMP.Command = smpReportGeneral + " " + device
	out, err := c.exec(ctx, smpReportGeneral, device)
	if err != nil {
		expander.SMP.Err = Some(err.Error())
		p.note(CollectorSAS, fmt.Errorf("%s: %w", expander.SMP.Command, err))
		return
	}
	expander.SMP.Available, expander.SMP.OK = true, true
	expander.NumPhys = parseSMPGeneral(out)
	discover := smpDiscover + " --multiple " + device
	list, err := c.exec(ctx, smpDiscover, "--multiple", device)
	if err != nil {
		// The expander answered the general request, so it is reachable;
		// only the per-phy description is missing. The phys are still
		// enumerated from the count it gave, because the error counters
		// are the other half of this feature and do not need discover.
		p.note(CollectorSAS, fmt.Errorf("%s: %w", discover, err))
		expander.Phys = smpPhyPlaceholders(expander.NumPhys, discover+": "+err.Error())
		return
	}
	expander.Phys = parseSMPDiscoverList(list, discover)
	if len(expander.Phys) == 0 {
		// smp_discover answered in a shape this parser does not know. The
		// phy list is rebuilt from the reported count rather than left
		// empty, so an unparsed line costs the attached addresses and not
		// the error counters as well.
		expander.Phys = smpPhyPlaceholders(expander.NumPhys,
			discover+": the output carried no phy this parser recognises")
	}
}

// smpPhyPlaceholders enumerates phys 0..n-1 with nothing known about them
// but their number, so their error logs can still be read.
func smpPhyPlaceholders(count Optional[int64], reason string) []SMPPhy {
	n, ok := count.Get()
	if !ok || n <= 0 || n > smpPhyLimit {
		return nil
	}
	phys := make([]SMPPhy, 0, n)
	for i := range n {
		phys = append(phys, SMPPhy{Identifier: i, State: PHYStateUnknown, Source: reason})
	}
	return phys
}

// smpPhyErrors reads the error log of one expander phy.
//
// The --zero option of smp_rep_phy_err_log clears the counters it reports;
// it is never passed, because collecting diagnostics must not destroy the
// history it is collected for (ROADMAP 6).
func (c *Client) smpPhyErrors(ctx context.Context, expander Expander, phy *SMPPhy, p *problems) {
	device := expander.SMPDevice.Or("")
	phyArg := "--phy=" + strconv.FormatInt(phy.Identifier, 10)
	command := smpPhyErrorLog + " " + phyArg + " " + device
	readAt := time.Now()
	out, err := c.exec(ctx, smpPhyErrorLog, phyArg, device)
	if err != nil {
		phy.Counters = ErrorCounters{Source: command, ReadAt: readAt, Err: Some(err.Error())}
		p.note(CollectorSAS, fmt.Errorf("%s: %w", command, err))
		return
	}
	counters, reported := parseSMPPhyErrorLog(out)
	counters.Source, counters.ReadAt = command, readAt
	if id, ok := reported.Get(); ok && id != phy.Identifier {
		// The response names the phy it is about. When that is not the phy
		// that was asked for, the numbers belong to a different link and
		// attributing them here would be worse than reporting none.
		phy.Counters = ErrorCounters{Source: command, ReadAt: readAt, Err: Some(fmt.Sprintf(
			"the response reports phy %d and phy %d was requested, so the counters were discarded",
			id, phy.Identifier))}
		p.note(CollectorSAS, fmt.Errorf("%s: response is for phy %d", command, id))
		return
	}
	if !counters.Present() {
		counters.Err = Some("the expander answered without any error counter this parser recognises")
	}
	phy.Counters = counters
}
