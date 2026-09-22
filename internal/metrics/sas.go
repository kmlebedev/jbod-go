// SPDX-License-Identifier: BSD-2-Clause

package metrics

import (
	"strconv"

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

// phyLabels address one phy. The SAS address is the identity, the name is
// the location, and both are here because an operator reads one and a
// dashboard joins on the other.
var phyLabels = []string{"host", "phy", "port", "sas_address", "device_type"}

var (
	descPHYInfo = prometheus.NewDesc(
		"jbod_sas_phy_info",
		"One SAS phy: its state and the rate it negotiated; always 1",
		append(append([]string{}, phyLabels...), "state", "negotiated_link_rate"), nil)
	descPHYLinkRate = prometheus.NewDesc(
		"jbod_sas_phy_negotiated_link_rate_gbps",
		"Rate the link came up at; absent when the transport reports no rate",
		phyLabels, nil)
	descPHYUp = prometheus.NewDesc(
		"jbod_sas_phy_up",
		"Whether the phy negotiated a link rate",
		phyLabels, nil)
)

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
	descs := []*prometheus.Desc{descPHYInfo, descPHYLinkRate, descPHYUp}
	for _, counter := range phyCounters {
		descs = append(descs, counter.desc)
	}
	return descs
}()

// collectPHYs publishes one host's SAS links.
func collectPHYs(s *sink, snapshot jbod.Snapshot) {
	for _, phy := range snapshot.PHYs {
		labels := []string{
			optionalNumber(phy.Host), phy.Name, phy.Port.Or(""),
			phy.SASAddress.Or(""), phy.DeviceType.Or(""),
		}
		s.gauge(descPHYInfo, 1, append(append([]string{}, labels...),
			string(phy.State), phy.Negotiated.Text.Or(""))...)
		s.gauge(descPHYUp, boolean(phy.State == jbod.PHYStateUp), labels...)
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
