// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// This file is the link between the host and the shelf rather than the
// shelf itself: the SAS phys the transport exposes, the address and the
// negotiated rate of each, and the error counters the hardware keeps for
// them (ROADMAP 6).
//
// Everything here comes from the Linux SAS transport class
// (drivers/scsi/scsi_transport_sas.c): /sys/class/sas_phy for the phys,
// /sys/class/sas_expander and /sys/class/sas_device for the expanders. The
// attribute names and the value spellings below are that driver's, and the
// error counters are the hardware's own — nothing here resets one.
//
// Two boundaries are stated rather than papered over. A phy the transport
// does not describe is unknown and never "down": the class exposes no
// attribute that distinguishes an unplugged cable from a value the driver
// did not fill in. And a phy of the host a shelf is attached through is not
// proof that it carries that shelf's traffic; tying a phy to an enclosure
// is topology, which is a later part of 1.3.

// DefaultSysClass is where Linux exposes the SAS transport classes.
const DefaultSysClass = "/sys/class"

// The class directories this file reads.
const (
	classSASPHY      = "sas_phy"
	classSASExpander = "sas_expander"
	classSASDevice   = "sas_device"
	classBSG         = "bsg"
)

// PHYState is what the transport says about one phy.
//
// Unknown is a state of its own and the default: the SAS transport reports
// "Unknown" for a phy it has no rate for, and that covers an empty
// connector, a link that never came up and a driver that does not fill the
// field in. Calling any of those "down" would be an observation nobody
// made.
type PHYState string

const (
	// PHYStateUp is a phy with a negotiated link rate.
	PHYStateUp PHYState = "up"
	// PHYStateDisabled is a phy the transport reports as disabled.
	PHYStateDisabled PHYState = "disabled"
	// PHYStateFailed is a phy whose link rate negotiation failed.
	PHYStateFailed PHYState = "failed"
	// PHYStateSpinupHold is a SATA phy held in spin-up hold.
	PHYStateSpinupHold PHYState = "spin-up hold"
	// PHYStateUnknown is a phy the transport did not describe.
	PHYStateUnknown PHYState = "unknown"
)

// PHYStates is every state, in the order reports render them, so a state
// that drops to zero keeps its place.
var PHYStates = []PHYState{PHYStateUp, PHYStateDisabled, PHYStateFailed, PHYStateSpinupHold, PHYStateUnknown}

// LinkRate is one link rate as the transport or SMP spells it, with the
// number pulled out of it when the spelling carries one.
//
// The text is kept because the spellings that carry no number are the
// interesting ones — "Phy disabled", "Link rate failed", "Spin-up hold" —
// and a rate of 0 Gbit/s would be a different claim entirely.
type LinkRate struct {
	Text Optional[string]  `json:"text"`
	Gbps Optional[float64] `json:"gbps"`
}

// linkRateGbps matches the numeric part of "12.0 Gbit", "6 Gbps" and
// "Phy enabled; 1.5 Gbps". The unit is required: a bare number on such a
// line is a phy identifier or a count, not a rate.
var linkRateGbps = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*g(?:bit|bps|b)\b`)

// parseLinkRate turns a link rate attribute into a LinkRate. An attribute
// that is not there stays absent in both halves.
func parseLinkRate(raw string, ok bool) LinkRate {
	if !ok {
		return LinkRate{}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return LinkRate{}
	}
	rate := LinkRate{Text: Some(raw)}
	if m := linkRateGbps.FindStringSubmatch(raw); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			rate.Gbps = Some(v)
		}
	}
	return rate
}

// phyState decides the state of a phy from its negotiated rate and, when
// the transport exposes one, its enable flag.
//
// The spellings that say what went wrong are read first: a phy reporting
// "Link rate failed" has failed whatever its enable flag says, and folding
// that into "not up" would lose the only diagnosis the transport offers.
func phyState(rate LinkRate, enabled Optional[bool]) PHYState {
	text := strings.ToLower(rate.Text.Or(""))
	switch {
	case strings.Contains(text, "disabled"):
		return PHYStateDisabled
	case strings.Contains(text, "failed"):
		return PHYStateFailed
	case strings.Contains(text, "spin"):
		return PHYStateSpinupHold
	}
	if off, ok := enabled.Get(); ok && !off {
		return PHYStateDisabled
	}
	if rate.Gbps.Present() {
		return PHYStateUp
	}
	return PHYStateUnknown
}

// ErrorCounters are the four link error counters SAS keeps per phy.
//
// They are the hardware's own running totals and are read, never cleared:
// smp_rep_phy_err_log has a --zero option that clears them as a side effect
// of reading, and using it would destroy the only history anybody has of a
// marginal cable (ROADMAP 6).
//
// Every field is optional because a source may report some and not others,
// and because a counter that was not read is not a counter at zero.
type ErrorCounters struct {
	// Source is where the numbers came from, so a value in a report can be
	// checked against the hardware by hand.
	Source string    `json:"source"`
	ReadAt time.Time `json:"read_at"`
	// InvalidDword counts dwords the phy could not decode, which is the
	// first thing a marginal cable or connector shows up as.
	InvalidDword Optional[int64] `json:"invalid_dword_count"`
	// RunningDisparityError counts 8b10b disparity errors.
	RunningDisparityError Optional[int64] `json:"running_disparity_error_count"`
	// LossOfDwordSync counts how often the phy lost dword synchronisation,
	// which is a link that dropped rather than a byte that was corrupted.
	LossOfDwordSync Optional[int64] `json:"loss_of_dword_sync_count"`
	// PhyResetProblem counts failed phy resets.
	PhyResetProblem Optional[int64] `json:"phy_reset_problem_count"`
	// Err is why the counters are absent, when the source failed.
	Err Optional[string] `json:"error"`
}

// Present reports whether any counter was read at all.
func (e ErrorCounters) Present() bool {
	return e.InvalidDword.Present() || e.RunningDisparityError.Present() ||
		e.LossOfDwordSync.Present() || e.PhyResetProblem.Present()
}

// Total sums the counters that were read. It is false when none was, so a
// phy nobody could ask never reports a clean zero.
func (e ErrorCounters) Total() (int64, bool) {
	total := int64(0)
	any := false
	for _, counter := range []Optional[int64]{
		e.InvalidDword, e.RunningDisparityError, e.LossOfDwordSync, e.PhyResetProblem,
	} {
		if v, ok := counter.Get(); ok {
			total += v
			any = true
		}
	}
	return total, any
}

// PHY is one SAS phy as the transport class describes it.
//
// The identity fields belong to the device that owns the phy: an expander
// phy carries the expander's SAS address, not the address of whatever is
// plugged into it. The far end is only known through SMP; see SMPPhy.
type PHY struct {
	// Name is the sysfs name ("phy-1:0", "phy-1:0:12"). It encodes the
	// SCSI host and is assigned at scan time, so it is a location and not
	// an identity — the SAS address is the identity.
	Name string `json:"name"`
	// Host is the SCSI host number this phy belongs to.
	Host Optional[int64] `json:"host"`
	// Parent is the sysfs name of the device that owns the phy: the SAS
	// host, an expander, or an end device.
	Parent Optional[string] `json:"parent"`
	// Port is the sas_port the phy is a member of, absent for a phy that
	// is in no port (a wide port is several phys in one).
	Port Optional[string] `json:"port"`
	// Identifier is the phy number within its own device, which is what
	// SMP addresses it by.
	Identifier Optional[int64] `json:"phy_identifier"`
	// SASAddress is the address of the device this phy belongs to.
	SASAddress Optional[string] `json:"sas_address"`
	// DeviceType is what that device is ("end device", "edge expander").
	DeviceType Optional[string] `json:"device_type"`
	// InitiatorProtocols and TargetProtocols are the protocols the phy
	// announced in its identify frame ("ssp, stp, smp", "none").
	InitiatorProtocols Optional[string] `json:"initiator_port_protocols"`
	TargetProtocols    Optional[string] `json:"target_port_protocols"`
	// Enabled is the transport's enable flag, absent when the driver does
	// not expose one.
	Enabled Optional[bool] `json:"enabled"`
	// State is the verdict; see PHYState.
	State PHYState `json:"state"`
	// Negotiated is the rate the link came up at, Minimum and Maximum the
	// programmed limits and the HW ones the hardware is capable of.
	Negotiated LinkRate `json:"negotiated_link_rate"`
	Minimum    LinkRate `json:"minimum_link_rate"`
	Maximum    LinkRate `json:"maximum_link_rate"`
	MinimumHW  LinkRate `json:"minimum_link_rate_hw"`
	MaximumHW  LinkRate `json:"maximum_link_rate_hw"`
	// Counters are the link error counters the transport exposes.
	Counters ErrorCounters `json:"error_counters"`
	// Source is the sysfs directory this phy was read from.
	Source string `json:"source"`
	// Err explains a phy whose directory could not be read.
	Err Optional[string] `json:"error"`
}

// Expander is one SAS expander, which is both a device in the topology and
// the target an SMP request is addressed to.
type Expander struct {
	Name string          `json:"name"`
	Host Optional[int64] `json:"host"`
	// SASAddress comes from the expander's entry in sas_device; the
	// expander class itself does not carry it.
	SASAddress        Optional[string] `json:"sas_address"`
	Vendor            Optional[string] `json:"vendor_id"`
	Product           Optional[string] `json:"product_id"`
	Revision          Optional[string] `json:"product_rev"`
	ComponentVendor   Optional[string] `json:"component_vendor_id"`
	ComponentID       Optional[int64]  `json:"component_id"`
	ComponentRevision Optional[int64]  `json:"component_revision_id"`
	// Level is how far the expander sits from the initiator.
	Level Optional[int64] `json:"level"`
	// SMPDevice is the bsg node smp_utils addresses this expander by,
	// absent when the kernel exposes none.
	SMPDevice Optional[string] `json:"smp_device"`
	// NumPhys is what the expander itself reports, which is the authority
	// on how many phys to ask about.
	NumPhys Optional[int64] `json:"num_phys"`
	// Phys are the phys as SMP describes them, empty when SMP was not
	// asked for or did not answer.
	Phys []SMPPhy `json:"smp_phys,omitempty"`
	// SMP says whether SMP was used for this expander and how it went.
	SMP SMPStatus `json:"smp"`
	// Source is the sysfs directory this expander was read from.
	Source string `json:"source"`
}

// SASReport is the transport view of one SCSI host.
//
// The grouping is the host and not the shelf on purpose: a chassis with two
// I/O modules registers two sysfs enclosures behind one HBA, and reporting
// the same phys twice would read as twice the hardware.
type SASReport struct {
	Host int64 `json:"host"`
	// Enclosures are the shelves whose SCSI address names this host. They
	// say which shelves are reached through it, not which phy carries
	// which shelf: that is topology (ROADMAP 6).
	Enclosures []string   `json:"enclosures,omitempty"`
	PHYs       []PHY      `json:"phys"`
	Expanders  []Expander `json:"expanders,omitempty"`
	// States counts the phys per state, so a report can be read without
	// counting rows.
	States     map[PHYState]int `json:"states"`
	Collection SASCollection    `json:"collection"`
}

// SASCollection says where a report came from and how complete it is,
// separately from what the hardware reported (ROADMAP 3).
type SASCollection struct {
	ReadAt time.Time `json:"read_at"`
	// Source is the class root the phys were read from.
	Source string `json:"source"`
	// Complete is true when every phy the class directory listed could be
	// read, and, when SMP was requested, every expander answered.
	Complete bool `json:"complete"`
	// Unreadable counts the phys whose directory could not be read.
	Unreadable int `json:"unreadable_phys"`
	// SMP says whether SMP was requested and whether it could be used.
	SMP SMPStatus `json:"smp"`
	// Err is why the class directory itself could not be walked, which is
	// also what a host without SAS transport looks like.
	Err Optional[string] `json:"error"`
}

// SASOptions selects how much work SAS does.
type SASOptions struct {
	// WithSMP also asks every expander for its phys and their error
	// counters, which costs one SMP request per phy. It is off by default:
	// a 38-phy expander is 38 requests through the same SMP processor, and
	// a scrape must not do that every interval (ROADMAP 6).
	WithSMP bool
}

// hostOfName reads the SCSI host number out of a transport object name:
// "phy-1:0", "phy-1:0:12", "expander-1:0" all start with it.
func hostOfName(name string) Optional[int64] {
	_, rest, ok := strings.Cut(name, "-")
	if !ok {
		return None[int64]()
	}
	digits, _, _ := strings.Cut(rest, ":")
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return None[int64]()
	}
	return Some(n)
}

// hostOfAddress reads the SCSI host number out of an "H:C:T:L" address,
// which is what the enclosure listing calls a slot.
func hostOfAddress(address string) Optional[int64] {
	digits, _, ok := strings.Cut(address, ":")
	if !ok {
		return None[int64]()
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return None[int64]()
	}
	return Some(n)
}

// classDir is one SAS transport class directory.
func (c *Client) classDir(class string) string { return filepath.Join(c.sysClass, class) }

// classNames lists the entries of one class directory, in natural order so
// phy-1:0:2 comes before phy-1:0:10.
func (c *Client) classNames(class string) ([]string, error) {
	entries, err := os.ReadDir(c.classDir(class))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.SortStableFunc(names, natCompare)
	return names, nil
}

// phyAncestors resolves a class entry to its real sysfs path and returns
// the device that owns it and the port it belongs to.
//
// Both are taken from the path because that is where the kernel puts the
// relationship: /sys/class/sas_phy/phy-1:0:12 points into
// .../host1/port-1:0/expander-1:0/phy-1:0:12. A path that cannot be
// resolved leaves both absent rather than guessing from the name.
func phyAncestors(path string) (parent, port Optional[string]) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return None[string](), None[string]()
	}
	parts := strings.Split(filepath.ToSlash(resolved), "/")
	if len(parts) >= 2 {
		parent = Some(parts[len(parts)-2])
	}
	for i := len(parts) - 2; i >= 0; i-- {
		if strings.HasPrefix(parts[i], "port-") {
			port = Some(parts[i])
			break
		}
	}
	return parent, port
}

// readPHY fills in one phy from its class directory.
func (c *Client) readPHY(name string, readAt time.Time) PHY {
	dir := filepath.Join(c.classDir(classSASPHY), name)
	parent, port := phyAncestors(dir)
	phy := PHY{
		Name:               name,
		Host:               hostOfName(name),
		Parent:             parent,
		Port:               port,
		Identifier:         From(readInt(filepath.Join(dir, "phy_identifier"))),
		SASAddress:         From(readSASAddress(filepath.Join(dir, "sas_address"))),
		DeviceType:         From(readText(filepath.Join(dir, "device_type"))),
		InitiatorProtocols: From(readText(filepath.Join(dir, "initiator_port_protocols"))),
		TargetProtocols:    From(readText(filepath.Join(dir, "target_port_protocols"))),
		Enabled:            From(readBool(filepath.Join(dir, "enable"))),
		Negotiated:         parseLinkRate(readText(filepath.Join(dir, "negotiated_linkrate"))),
		Minimum:            parseLinkRate(readText(filepath.Join(dir, "minimum_linkrate"))),
		Maximum:            parseLinkRate(readText(filepath.Join(dir, "maximum_linkrate"))),
		MinimumHW:          parseLinkRate(readText(filepath.Join(dir, "minimum_linkrate_hw"))),
		MaximumHW:          parseLinkRate(readText(filepath.Join(dir, "maximum_linkrate_hw"))),
		Source:             "sysfs: " + dir,
	}
	phy.State = phyState(phy.Negotiated, phy.Enabled)
	phy.Counters = readCounters(dir, readAt)
	// A directory with none of the identity attributes is not a phy this
	// package can describe, and the reason travels with it.
	if !phy.SASAddress.Present() && !phy.Identifier.Present() && !phy.Negotiated.Text.Present() {
		phy.Err = Some("the phy exposes neither an address, a phy identifier nor a link rate; " +
			"either the driver publishes no SAS transport attributes or they could not be read")
	}
	return phy
}

// counterAttributes are the four link error counters of the SAS transport
// class, in the order the reports render them.
var counterAttributes = []struct {
	name string
	set  func(*ErrorCounters, Optional[int64])
}{
	{"invalid_dword_count", func(e *ErrorCounters, v Optional[int64]) { e.InvalidDword = v }},
	{"running_disparity_error_count", func(e *ErrorCounters, v Optional[int64]) { e.RunningDisparityError = v }},
	{"loss_of_dword_sync_count", func(e *ErrorCounters, v Optional[int64]) { e.LossOfDwordSync = v }},
	{"phy_reset_problem_count", func(e *ErrorCounters, v Optional[int64]) { e.PhyResetProblem = v }},
}

// readCounter reads one link error counter and keeps the reason it could
// not be read.
//
// The reason matters because the two ways it fails are different findings.
// The attribute is created for every phy of the class, so a missing file
// means the kernel does not have the feature at all; a file that exists and
// fails to read means the driver tried. For an expander phy the driver
// answers by asking the expander for that phy's error log, and that request
// fails on its own — a phy with nothing attached commonly returns EIO —
// which is one phy we could not ask, not a driver without counters.
func readCounter(path string) (Optional[int64], error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return None[int64](), err
	}
	text := strings.TrimSpace(string(raw))
	n, convErr := strconv.ParseInt(text, 10, 64)
	if convErr != nil {
		return None[int64](), fmt.Errorf("%s: %q is not a number", path, text)
	}
	return Some(n), nil
}

// readCounters reads all four counters of one phy and says what happened to
// the ones that are absent.
//
// Nothing here is counted as a collection failure. On a 68-phy expander the
// unattached phys answer EIO, and counting each of them every scrape would
// turn jbod_scrape_errors_total into a number that only says how many empty
// connectors the shelf has. The absence is already fully expressed: no
// value, no metric series, and the reason on the phy.
func readCounters(dir string, readAt time.Time) ErrorCounters {
	counters := ErrorCounters{Source: "sysfs: " + dir, ReadAt: readAt}
	var first error
	missing, failed := 0, 0
	for _, attribute := range counterAttributes {
		value, err := readCounter(filepath.Join(dir, attribute.name))
		attribute.set(&counters, value)
		switch {
		case err == nil:
		case os.IsNotExist(err):
			missing++
		default:
			failed++
		}
		if err != nil && first == nil {
			first = err
		}
	}
	switch {
	case failed == 0 && missing == 0:
		return counters
	case failed > 0:
		counters.Err = Some(fmt.Sprintf(
			"%d of the %d link error counters exist and could not be read (%v); the driver answers "+
				"this attribute by asking the expander for that phy's error log, which fails on its own "+
				"for a phy with nothing attached",
			failed, len(counterAttributes), first))
	default:
		counters.Err = Some(fmt.Sprintf(
			"%d of the %d link error counters are not exposed at all (%v); this driver publishes none",
			missing, len(counterAttributes), first))
	}
	return counters
}

// readSASAddress reads an address attribute and normalises it to lower case
// hex with the 0x prefix the transport prints, so the same address compares
// equal wherever it was read.
func readSASAddress(path string) (string, bool) {
	raw, ok := readText(path)
	if !ok {
		return "", false
	}
	address := strings.ToLower(strings.TrimSpace(raw))
	if address == "" {
		return "", false
	}
	if nullAddress(address) {
		// A phy with nothing behind it reports the null address, which is
		// not an identity: published as one it would make every unused
		// phy look like the same device.
		return "", false
	}
	if !strings.HasPrefix(address, "0x") {
		address = "0x" + address
	}
	return address, true
}

// readExpander fills in one expander from its class directory.
func (c *Client) readExpander(name string) Expander {
	dir := filepath.Join(c.classDir(classSASExpander), name)
	expander := Expander{
		Name:              name,
		Host:              hostOfName(name),
		Vendor:            From(readText(filepath.Join(dir, "vendor_id"))),
		Product:           From(readText(filepath.Join(dir, "product_id"))),
		Revision:          From(readText(filepath.Join(dir, "product_rev"))),
		ComponentVendor:   From(readText(filepath.Join(dir, "component_vendor_id"))),
		ComponentID:       From(readInt(filepath.Join(dir, "component_id"))),
		ComponentRevision: From(readInt(filepath.Join(dir, "component_revision_id"))),
		Level:             From(readInt(filepath.Join(dir, "level"))),
		// The expander's own address lives on its remote phy, which the
		// kernel files under sas_device rather than sas_expander.
		SASAddress: From(readSASAddress(filepath.Join(c.classDir(classSASDevice), name, "sas_address"))),
		SMPDevice:  c.smpDevice(name),
		Source:     "sysfs: " + dir,
	}
	return expander
}

// smpDevice returns the bsg node an SMP request is addressed to, when the
// kernel exposes one for this expander.
func (c *Client) smpDevice(name string) Optional[string] {
	if _, err := os.Stat(filepath.Join(c.classDir(classBSG), name)); err != nil {
		return None[string]()
	}
	return Some(filepath.Join(c.bsgDir, name))
}

// SAS reads the SAS transport view of the hosts the given enclosures are
// attached through, or of every host when no enclosure is given.
//
// The sysfs half costs no external commands at all: the phys, their
// addresses, their rates and their error counters are attribute reads. SMP
// is opt-in through opts and costs one request per phy (ROADMAP 6).
func (c *Client) SAS(ctx context.Context, enclosures []Enclosure, opts SASOptions) ([]SASReport, error) {
	p := newProblems(c.logger)
	reports := c.sas(ctx, enclosures, opts, p)
	if err := p.err(); err != nil {
		return reports, err
	}
	return reports, ctx.Err()
}

// sasHosts returns the hosts to report on and the shelves reached through
// each of them.
func sasHosts(enclosures []Enclosure, phys []PHY) ([]int64, map[int64][]string) {
	shelves := map[int64][]string{}
	for _, enc := range enclosures {
		if host, ok := hostOfAddress(enc.Slot).Get(); ok {
			shelves[host] = append(shelves[host], enc.Slot)
		}
	}
	var hosts []int64
	seen := map[int64]bool{}
	add := func(host int64) {
		if seen[host] {
			return
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	if len(shelves) > 0 {
		// A shelf was named, so only the hosts it is reached through are
		// reported; the other HBAs of the machine are not its business.
		for host := range shelves {
			add(host)
		}
		slices.Sort(hosts)
		return hosts, shelves
	}
	for _, phy := range phys {
		if host, ok := phy.Host.Get(); ok {
			add(host)
		}
	}
	slices.Sort(hosts)
	return hosts, shelves
}

func (c *Client) sas(ctx context.Context, enclosures []Enclosure, opts SASOptions, p *problems) []SASReport {
	readAt := time.Now()
	phys, walkErr := c.sasPHYs(readAt, p)
	expanders := c.sasExpanders(p)
	hosts, shelves := sasHosts(enclosures, phys)
	reports := make([]SASReport, 0, len(hosts))
	for _, host := range hosts {
		report := SASReport{
			Host:       host,
			Enclosures: shelves[host],
			States:     map[PHYState]int{},
			Collection: SASCollection{ReadAt: readAt, Source: c.classDir(classSASPHY), Err: walkErr},
		}
		for _, phy := range phys {
			if phy.Host.Or(-1) != host {
				continue
			}
			report.PHYs = append(report.PHYs, phy)
			report.States[phy.State]++
			if phy.Err.Present() {
				report.Collection.Unreadable++
			}
		}
		for _, expander := range expanders {
			if expander.Host.Or(-1) == host {
				report.Expanders = append(report.Expanders, expander)
			}
		}
		reports = append(reports, report)
	}
	if opts.WithSMP {
		c.addSMP(ctx, reports, p)
	}
	for i := range reports {
		reports[i].Collection.Complete = sasComplete(reports[i])
	}
	return reports
}

// sasComplete reports whether everything this report set out to read was
// read. A host with no SAS transport at all is not complete: there is
// nothing to be complete about, and saying so is not the same as saying the
// links are fine.
func sasComplete(report SASReport) bool {
	if report.Collection.Err.Present() || len(report.PHYs) == 0 {
		return false
	}
	if report.Collection.Unreadable > 0 {
		return false
	}
	if report.Collection.SMP.Requested && !report.Collection.SMP.Available {
		return false
	}
	for _, expander := range report.Expanders {
		if expander.SMP.Requested && !expander.SMP.OK {
			return false
		}
	}
	return true
}

// sasPHYs reads every phy of the machine. The error is returned rather than
// counted when the class directory itself is missing: a host without SAS
// transport is the normal case for a SATA or USB enclosure, and counting it
// as a collection failure would make every such machine look broken.
func (c *Client) sasPHYs(readAt time.Time, p *problems) ([]PHY, Optional[string]) {
	names, err := c.classNames(classSASPHY)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, Some(c.classDir(classSASPHY) + " does not exist: this host exposes no SAS transport")
		}
		p.note(CollectorSAS, fmt.Errorf("read %s: %w", c.classDir(classSASPHY), err))
		return nil, Some(err.Error())
	}
	phys := make([]PHY, 0, len(names))
	for _, name := range names {
		phy := c.readPHY(name, readAt)
		if reason, unreadable := phy.Err.Get(); unreadable {
			p.note(CollectorSAS, fmt.Errorf("%s: %s", name, reason))
		}
		phys = append(phys, phy)
	}
	return phys, None[string]()
}

// sasExpanders reads every expander of the machine.
func (c *Client) sasExpanders(p *problems) []Expander {
	names, err := c.classNames(classSASExpander)
	if err != nil {
		if !os.IsNotExist(err) {
			p.note(CollectorSAS, fmt.Errorf("read %s: %w", c.classDir(classSASExpander), err))
		}
		return nil
	}
	expanders := make([]Expander, 0, len(names))
	for _, name := range names {
		expanders = append(expanders, c.readExpander(name))
	}
	return expanders
}
