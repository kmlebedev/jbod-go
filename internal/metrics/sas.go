// SPDX-License-Identifier: BSD-2-Clause

package metrics

import (
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// The v1.3 series: the SAS links behind the shelves (ROADMAP 6).
//
// Three decisions shape them:
//
//   - The error counters are published as counters, with the hardware's own
//     running total as the value. They are reset by a phy reset, an HBA
//     reload or a reboot, and Prometheus already knows what to do with a
//     counter that goes backwards: rate() and increase() read a drop as a
//     reset and never produce a negative increase. Accumulating them here
//     instead would be wrong across a restart of the exporter, which does
//     not survive to remember the previous total.
//
//   - A counter the transport did not expose gets no series. A zero is a
//     clean link, and a driver that publishes no counters has not said the
//     link is clean.
//
//   - The labels are the host and the phy, not the enclosure. A phy belongs
//     to the HBA; which phy carries which shelf is topology, and a label
//     that claimed it would be a claim nobody checked.
//
// Two more shape the state:
//
//   - The state is a state set, one series per state with 1 on the state the
//     transport reports. "Up or not" folded disabled, failed and unknown
//     into one 0, and those are the three diagnoses the transport offers;
//     a label on the info series instead would end one series and start
//     another at the moment a link changes, which is the moment a dashboard
//     is looked at.
//
//   - A phy the expander declined to describe (jbod.PHY.Unanswered, the
//     sysfs face of a vacant phy) gets no per-phy series. On a real shelf
//     that was 192 of 391 phys, each an info series and a state series
//     saying nothing. They are counted per expander instead, so a number
//     that changes — an expander that stops describing phys it used to —
//     is still seen.

// phyLabels address one phy. The SAS address is the identity, the name is
// the location, and both are here because an operator reads one and a
// dashboard joins on the other.
var phyLabels = []string{"host", "phy", "port", "sas_address", "device_type"}

var (
	descPHYInfo = prometheus.NewDesc(
		"jbod_sas_phy_info",
		"One SAS phy and the rate it negotiated, as the transport spells it; always 1",
		append(append([]string{}, phyLabels...), "negotiated_link_rate"), nil)
	descPHYLinkRate = prometheus.NewDesc(
		"jbod_sas_phy_negotiated_link_rate_gbps",
		"Rate the link came up at; absent when the transport reports no rate",
		phyLabels, nil)
	descPHYState = prometheus.NewDesc(
		"jbod_sas_phy_state",
		"Condition of one phy; 1 marks the state the transport reports, the others are 0",
		append(append([]string{}, phyLabels...), "state"), nil)
	descPHYsUnanswered = prometheus.NewDesc(
		"jbod_sas_expander_phys_unanswered",
		"Phys of an expander that have no rate and whose error log the expander refused, "+
			"which is how a vacant phy reads in sysfs; they get no per-phy series",
		[]string{"host", "sas_address"}, nil)
)

// phyStates are the states the state set publishes: every state a phy that
// gets series can be in. Vacant is not one of them — a vacant phy gets no
// series (see collectPHYs) — so each published phy has exactly one 1.
var phyStates = func() []jbod.PHYState {
	states := make([]jbod.PHYState, 0, len(jbod.PHYStates))
	for _, state := range jbod.PHYStates {
		if state != jbod.PHYStateVacant {
			states = append(states, state)
		}
	}
	return states
}()

// phyCounters are the four link error counters, each with the accessor that
// reads it, so adding one is a single entry.
var phyCounters = []struct {
	desc *prometheus.Desc
	pick func(jbod.ErrorCounters) jbod.Optional[int64]
}{
	{
		prometheus.NewDesc("jbod_sas_phy_invalid_dword_total",
			"Dwords the phy could not decode, as the hardware counts them", phyLabels, nil),
		func(e jbod.ErrorCounters) jbod.Optional[int64] { return e.InvalidDword },
	},
	{
		prometheus.NewDesc("jbod_sas_phy_running_disparity_error_total",
			"Running disparity errors, as the hardware counts them", phyLabels, nil),
		func(e jbod.ErrorCounters) jbod.Optional[int64] { return e.RunningDisparityError },
	},
	{
		prometheus.NewDesc("jbod_sas_phy_loss_of_dword_sync_total",
			"Times the phy lost dword synchronisation, as the hardware counts them", phyLabels, nil),
		func(e jbod.ErrorCounters) jbod.Optional[int64] { return e.LossOfDwordSync },
	},
	{
		prometheus.NewDesc("jbod_sas_phy_reset_problem_total",
			"Phy reset problems, as the hardware counts them", phyLabels, nil),
		func(e jbod.ErrorCounters) jbod.Optional[int64] { return e.PhyResetProblem },
	},
}

// sasDescriptors is what this file can publish, for Describe.
var sasDescriptors = func() []*prometheus.Desc {
	descs := []*prometheus.Desc{descPHYInfo, descPHYLinkRate, descPHYState, descPHYsUnanswered}
	for _, counter := range phyCounters {
		descs = append(descs, counter.desc)
	}
	return descs
}()

// collectPHYs publishes one host's SAS links.
func collectPHYs(s *sink, snapshot jbod.Snapshot) {
	unanswered := map[expanderKey]int{}
	var expanders []expanderKey
	for _, phy := range snapshot.PHYs {
		if expander, ok := expanderOf(phy); ok {
			if _, seen := unanswered[expander]; !seen {
				unanswered[expander] = 0
				expanders = append(expanders, expander)
			}
			if phy.Unanswered() {
				unanswered[expander]++
			}
		}
		// A vacant phy is only ever reported over SMP, which the exporter
		// does not speak; the check keeps the rule whole if it ever does.
		if phy.Unanswered() || phy.State == jbod.PHYStateVacant {
			continue
		}
		labels := []string{
			optionalNumber(phy.Host), phy.Name, phy.Port.Or(""),
			phy.SASAddress.Or(""), phy.DeviceType.Or(""),
		}
		s.gauge(descPHYInfo, 1, append(append([]string{}, labels...),
			phy.Negotiated.Text.Or(""))...)
		for _, state := range phyStates {
			s.gauge(descPHYState, boolean(phy.State == state),
				append(append([]string{}, labels...), string(state))...)
		}
		if rate, ok := phy.Negotiated.Gbps.Get(); ok {
			// A phy that reports "Phy disabled" has no rate, and 0 Gbit/s
			// is a different claim: it gets no series at all.
			s.gauge(descPHYLinkRate, rate, labels...)
		}
		for _, counter := range phyCounters {
			if v, ok := counter.pick(phy.Counters).Get(); ok {
				s.counter(counter.desc, float64(v), labels...)
			}
		}
	}
	// Every expander with a phy in the snapshot gets its count, zero
	// included: a series that is there and 0 is an expander that described
	// every phy, and one that appears only once something goes wrong would
	// have no history to compare with.
	for _, expander := range expanders {
		s.gauge(descPHYsUnanswered, float64(unanswered[expander]), expander.host, expander.address)
	}
}

// expanderKey addresses one expander by the labels its count is published
// under.
type expanderKey struct{ host, address string }

// expanderOf returns the expander a phy belongs to. The address of an
// expander phy is the expander's own, so the phys of one expander share it;
// a phy without one cannot be counted against anything.
func expanderOf(phy jbod.PHY) (expanderKey, bool) {
	address, ok := phy.SASAddress.Get()
	if !ok || !strings.Contains(phy.DeviceType.Or(""), "expander") {
		return expanderKey{}, false
	}
	return expanderKey{optionalNumber(phy.Host), address}, true
}

// optionalNumber renders an integer label value, empty when the value is
// absent: an empty label is how Prometheus spells "no value", and 0 would
// be host zero.
func optionalNumber(v jbod.Optional[int64]) string {
	n, ok := v.Get()
	if !ok {
		return ""
	}
	return strconv.FormatInt(n, 10)
}
