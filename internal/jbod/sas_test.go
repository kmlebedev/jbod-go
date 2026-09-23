package jbod

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// links builds a /sys/class tree with the SAS transport shapes v1.3 has to
// tell apart:
//
//	phy-1:0    a host phy in a port, up at 12 Gbit, counters all zero
//	phy-1:1    a host phy the transport reports as disabled, with errors
//	phy-1:0:0  an expander phy whose driver publishes no error counters
//	phy-1:0:1  an expander phy with no rate: unknown, not down
//	phy-1:0:2  a directory with none of the attributes at all
//	phy-1:0:3  a phy whose counter attributes exist and cannot be read
//	phy-2:0    a phy of another HBA, which host selection must not return
//
// The phys are created under a devices tree and symlinked into the class
// directory the way sysfs really does it — <device>/phy-X/sas_phy/phy-X —
// because the parent and the port are read from the resolved path. A
// fixture that skipped the class directory hid a bug that reported
// "sas_phy" as the parent of every phy on real hardware.
func links(t *testing.T) *Client {
	t.Helper()
	root := t.TempDir()
	devices := filepath.Join(root, "devices")
	class := filepath.Join(root, "class")

	type phySpec struct {
		name       string
		path       string
		attributes map[string]string
	}
	up := map[string]string{
		"sas_address": "0x500605b00b1e2f40", "device_type": "end device", "phy_identifier": "0",
		"enable": "1", "negotiated_linkrate": "12.0 Gbit", "minimum_linkrate": "1.5 Gbit",
		"maximum_linkrate": "12.0 Gbit", "minimum_linkrate_hw": "1.5 Gbit", "maximum_linkrate_hw": "12.0 Gbit",
		"initiator_port_protocols": "ssp, stp, smp", "target_port_protocols": "none",
		"invalid_dword_count": "0", "running_disparity_error_count": "0",
		"loss_of_dword_sync_count": "0", "phy_reset_problem_count": "0",
	}
	disabled := map[string]string{
		"sas_address": "0x500605b00b1e2f41", "device_type": "end device", "phy_identifier": "1",
		"enable": "0", "negotiated_linkrate": "Phy disabled", "maximum_linkrate": "12.0 Gbit",
		"invalid_dword_count": "1274", "running_disparity_error_count": "7",
		"loss_of_dword_sync_count": "31", "phy_reset_problem_count": "2",
	}
	noCounters := map[string]string{
		"sas_address": "0x5000ccab05629d3f", "device_type": "edge expander", "phy_identifier": "0",
		"enable": "1", "negotiated_linkrate": "6.0 Gbit", "maximum_linkrate": "12.0 Gbit",
	}
	noRate := map[string]string{
		"sas_address": "0x5000ccab05629d3f", "device_type": "edge expander", "phy_identifier": "1",
		"enable": "1", "negotiated_linkrate": "Unknown",
		"invalid_dword_count": "0", "running_disparity_error_count": "0",
		"loss_of_dword_sync_count": "0", "phy_reset_problem_count": "0",
	}
	other := map[string]string{
		"sas_address": "0x500605b00b1e2f50", "device_type": "end device", "phy_identifier": "0",
		"enable": "1", "negotiated_linkrate": "6.0 Gbit",
		"invalid_dword_count": "0", "running_disparity_error_count": "0",
		"loss_of_dword_sync_count": "0", "phy_reset_problem_count": "0",
	}
	// An expander phy whose counters exist and answer with an error. The
	// driver serves them by asking the expander for that phy's error log,
	// and on a real shelf the phys that failed this way were exactly the
	// ones SMP reports as vacant; a directory in place of the file
	// reproduces a read that fails without the file being absent.
	unreadable := map[string]string{
		"sas_address": "0x5000ccab05629d3f", "device_type": "edge expander", "phy_identifier": "3",
		"enable": "1", "negotiated_linkrate": "Unknown",
	}
	expanderDir := "host1/port-1:0/expander-1:0"
	phys := []phySpec{
		{"phy-1:0", "host1/port-1:0", up},
		{"phy-1:1", "host1", disabled},
		{"phy-1:0:0", expanderDir, noCounters},
		{"phy-1:0:1", expanderDir, noRate},
		{"phy-1:0:2", expanderDir, map[string]string{}},
		{"phy-1:0:3", expanderDir, unreadable},
		{"phy-2:0", "host2", other},
	}
	mkdir(t, filepath.Join(class, "sas_phy"))
	for _, spec := range phys {
		dir := filepath.Join(devices, spec.path, spec.name, classSASPHY, spec.name)
		mkdir(t, dir)
		for name, value := range spec.attributes {
			write(t, filepath.Join(dir, name), value, 0o444)
		}
		if spec.name == "phy-1:0:3" {
			for _, name := range []string{
				"invalid_dword_count", "running_disparity_error_count",
				"loss_of_dword_sync_count", "phy_reset_problem_count",
			} {
				mkdir(t, filepath.Join(dir, name))
			}
		}
		symlink(t, dir, filepath.Join(class, "sas_phy", spec.name))
	}

	// One expander, reachable over SMP, plus its entry in sas_device where
	// the kernel keeps its address.
	mkdir(t, filepath.Join(class, "sas_expander"))
	expander := filepath.Join(devices, expanderDir)
	for name, value := range map[string]string{
		"vendor_id": "HGST", "product_id": "H4060-J", "product_rev": "4013",
		"component_vendor_id": "LSI", "component_id": "560", "component_revision_id": "5", "level": "1",
	} {
		write(t, filepath.Join(expander, name), value, 0o444)
	}
	symlink(t, expander, filepath.Join(class, "sas_expander", "expander-1:0"))
	device := filepath.Join(devices, expanderDir, "sas_device")
	mkdir(t, device)
	write(t, filepath.Join(device, "sas_address"), "0x5000ccab05629d3f", 0o444)
	mkdir(t, filepath.Join(class, "sas_device"))
	symlink(t, device, filepath.Join(class, "sas_device", "expander-1:0"))
	mkdir(t, filepath.Join(class, "bsg"))
	write(t, filepath.Join(class, "bsg", "expander-1:0"), "", 0o444)

	return New(WithSysClass(class), WithBSGDir("/dev/bsg"),
		WithRunner(func(context.Context, string, ...string) (string, error) {
			t.Error("the sysfs transport must not run an external command")
			return "", nil
		}))
}

// symlink points name at target, the way /sys/class points at /sys/devices.
func symlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

// shelfOn returns an enclosure whose SCSI address names the given host, so
// the selection under test has something to resolve.
func shelfOn(address string) Enclosure {
	return Enclosure{Slot: address, Device: "/dev/sg0", ID: Some("0x5000ccab05629d00")}
}

// TestSASPHYs covers the first half of ROADMAP 6: the address, the
// negotiated rate and the state of every phy, read from the transport class
// without running anything.
func TestSASPHYs(t *testing.T) {
	t.Parallel()
	reports, err := links(t).SAS(context.Background(), []Enclosure{shelfOn("1:0:0:0")}, SASOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Host != 1 {
		t.Fatalf("selecting a shelf of host 1 gave %+v", reports)
	}
	report := reports[0]
	// The phy of host 2 belongs to another HBA and must not appear.
	if len(report.PHYs) != 6 {
		t.Fatalf("got %d phys, want the 6 of host 1: %+v", len(report.PHYs), report.PHYs)
	}
	byName := map[string]PHY{}
	for _, phy := range report.PHYs {
		byName[phy.Name] = phy
	}
	up := byName["phy-1:0"]
	if address, _ := up.SASAddress.Get(); address != "0x500605b00b1e2f40" {
		t.Errorf("phy-1:0 address %s", up.SASAddress)
	}
	if up.State != PHYStateUp {
		t.Errorf("phy-1:0 state %s, want up", up.State)
	}
	if gbps, _ := up.Negotiated.Gbps.Get(); gbps != 12 {
		t.Errorf("phy-1:0 negotiated %v, want 12 Gbit/s", up.Negotiated)
	}
	if port, _ := up.Port.Get(); port != "port-1:0" {
		t.Errorf("phy-1:0 port %s, want port-1:0", up.Port)
	}
	if parent, _ := up.Parent.Get(); parent != "port-1:0" {
		t.Errorf("phy-1:0 parent %s", up.Parent)
	}
	// A disabled phy is disabled, not down and not up: the transport said
	// so, and its counters are still the interesting part.
	off := byName["phy-1:1"]
	if off.State != PHYStateDisabled {
		t.Errorf("phy-1:1 state %s, want disabled", off.State)
	}
	if off.Port.Present() {
		t.Errorf("phy-1:1 is in no port, got %s", off.Port)
	}
	if n, _ := off.Counters.InvalidDword.Get(); n != 1274 {
		t.Errorf("phy-1:1 invalid dwords %s, want 1274", off.Counters.InvalidDword)
	}
	if total, ok := off.Counters.Total(); !ok || total != 1274+7+31+2 {
		t.Errorf("phy-1:1 total %d (%v)", total, ok)
	}
	// A driver that publishes no counters must not read as a clean link.
	bare := byName["phy-1:0:0"]
	if bare.Counters.Present() {
		t.Errorf("phy-1:0:0 reports counters it does not have: %+v", bare.Counters)
	}
	if _, ok := bare.Counters.Total(); ok {
		t.Error("phy-1:0:0 produced a total from no counters")
	}
	// The two ways a counter goes missing are different findings, and the
	// reason has to tell them apart: a driver that publishes none, and a
	// phy the driver could not ask (ROADMAP 6, hardware run).
	reason, _ := bare.Counters.Err.Get()
	if !strings.Contains(reason, "not exposed at all") {
		t.Errorf("phy-1:0:0 counters: %q", reason)
	}
	unreadable := byName["phy-1:0:3"]
	if unreadable.Counters.Present() {
		t.Errorf("phy-1:0:3 produced counters from a failed read: %+v", unreadable.Counters)
	}
	failed, _ := unreadable.Counters.Err.Get()
	if !strings.Contains(failed, "could not be read") {
		t.Errorf("phy-1:0:3 counters: %q", failed)
	}
	if strings.Contains(failed, "publishes none") {
		t.Errorf("a failed read was reported as a driver without counters: %q", failed)
	}
	if parent, _ := bare.Parent.Get(); parent != "expander-1:0" {
		t.Errorf("phy-1:0:0 parent %s, want expander-1:0", bare.Parent)
	}
	// "Unknown" is unknown. It covers an empty connector and a field the
	// driver did not fill in, and neither of them is "down".
	unknown := byName["phy-1:0:1"]
	if unknown.State != PHYStateUnknown {
		t.Errorf("phy-1:0:1 state %s, want unknown", unknown.State)
	}
	if unknown.Negotiated.Gbps.Present() {
		t.Errorf("phy-1:0:1 invented a rate: %v", unknown.Negotiated)
	}
	// Only the phy whose four counters all exist and all fail is one the
	// expander declined to describe. A driver without counters (phy-1:0:0),
	// a phy with no link that still answered (phy-1:0:1) and a directory
	// with nothing in it (phy-1:0:2) are each something else.
	if unreadable.Counters.Failed != 4 {
		t.Errorf("phy-1:0:3 failed reads %d, want 4", unreadable.Counters.Failed)
	}
	if bare.Counters.Failed != 0 {
		t.Errorf("phy-1:0:0 counted missing attributes as failed reads: %d", bare.Counters.Failed)
	}
	for name, want := range map[string]bool{
		"phy-1:0": false, "phy-1:1": false, "phy-1:0:0": false,
		"phy-1:0:1": false, "phy-1:0:2": false, "phy-1:0:3": true,
	} {
		if got := byName[name].Unanswered(); got != want {
			t.Errorf("%s unanswered = %v, want %v", name, got, want)
		}
	}
	// A directory with no attributes is a phy nobody could describe.
	if !byName["phy-1:0:2"].Err.Present() {
		t.Error("an attribute-less phy is reported as if it had been read")
	}
	if report.Collection.Unreadable != 1 || report.Collection.Complete {
		t.Errorf("collection %+v, want one unreadable phy and an incomplete report", report.Collection)
	}
	// Two links up, one disabled, and two the transport did not describe:
	// the unknown pair is the point, a phy without a rate is counted and
	// never folded into the healthy ones.
	if report.States[PHYStateUp] != 2 || report.States[PHYStateDisabled] != 1 || report.States[PHYStateUnknown] != 3 {
		t.Errorf("state counts %v", report.States)
	}
	if len(report.Expanders) != 1 || report.Expanders[0].Name != "expander-1:0" {
		t.Fatalf("expanders %+v", report.Expanders)
	}
	expander := report.Expanders[0]
	if address, _ := expander.SASAddress.Get(); address != "0x5000ccab05629d3f" {
		t.Errorf("expander address %s", expander.SASAddress)
	}
	if device, _ := expander.SMPDevice.Get(); device != "/dev/bsg/expander-1:0" {
		t.Errorf("expander smp device %s", expander.SMPDevice)
	}
	if product, _ := expander.Product.Get(); product != "H4060-J" {
		t.Errorf("expander product %s", expander.Product)
	}
	// SMP was not asked for, so nothing about it is claimed either way.
	if report.Collection.SMP.Requested {
		t.Errorf("SMP was not requested but the report says it was: %+v", report.Collection.SMP)
	}
}

// TestSASEveryHost covers the listing without a shelf: every host that has
// phys is reported, in order.
func TestSASEveryHost(t *testing.T) {
	t.Parallel()
	reports, err := links(t).SAS(context.Background(), nil, SASOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || reports[0].Host != 1 || reports[1].Host != 2 {
		t.Fatalf("got %d reports: %+v", len(reports), reports)
	}
	if len(reports[1].PHYs) != 1 || len(reports[1].Expanders) != 0 {
		t.Errorf("host 2 got %d phys and %d expanders", len(reports[1].PHYs), len(reports[1].Expanders))
	}
	if len(reports[0].Enclosures) != 0 {
		t.Errorf("no shelf was named, so none should be listed: %v", reports[0].Enclosures)
	}
}

// TestSASWithoutTransport covers the common case this must not turn into an
// alarm: a machine whose enclosure is not behind SAS at all.
func TestSASWithoutTransport(t *testing.T) {
	t.Parallel()
	c := New(WithSysClass(filepath.Join(t.TempDir(), "absent")),
		WithRunner(func(context.Context, string, ...string) (string, error) { return "", nil }))
	reports, err := c.SAS(context.Background(), []Enclosure{shelfOn("3:0:0:0")}, SASOptions{})
	if err != nil {
		t.Fatalf("a host without SAS transport is not an error: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("a named shelf still gets a report: %+v", reports)
	}
	if len(reports[0].PHYs) != 0 || reports[0].Collection.Complete {
		t.Errorf("collection %+v", reports[0].Collection)
	}
	reason, ok := reports[0].Collection.Err.Get()
	if !ok {
		t.Fatal("no reason is given for the absent transport")
	}
	if reports[0].Collection.Unreadable != 0 {
		t.Errorf("nothing was unreadable, %d claimed", reports[0].Collection.Unreadable)
	}
	t.Log(reason)
}

// TestLinkRateAndState pins the spellings the transport uses, because every
// one of them that this package does not know becomes a phy with no state.
func TestLinkRateAndState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw     string
		present bool
		gbps    float64
		enabled Optional[bool]
		state   PHYState
	}{
		{"12.0 Gbit", true, 12, Some(true), PHYStateUp},
		{"1.5 Gbit", true, 1.5, None[bool](), PHYStateUp},
		{"Phy enabled; 6 Gbps", true, 6, Some(true), PHYStateUp},
		{"Phy disabled", true, 0, Some(false), PHYStateDisabled},
		{"Link rate failed", true, 0, Some(true), PHYStateFailed},
		{"Spin-up hold", true, 0, Some(true), PHYStateSpinupHold},
		{"Unknown", true, 0, Some(true), PHYStateUnknown},
		{"", false, 0, Some(true), PHYStateUnknown},
		// An enable flag of 0 with no rate at all is still a disabled phy.
		{"", false, 0, Some(false), PHYStateDisabled},
		// A rate the hardware negotiated wins over a stale enable flag:
		// "failed" is a diagnosis and "not enabled" is not.
		{"Link rate failed", true, 0, Some(false), PHYStateFailed},
	}
	for _, c := range cases {
		rate := parseLinkRate(c.raw, c.raw != "")
		if rate.Text.Present() != c.present {
			t.Errorf("%q: text %v", c.raw, rate.Text)
		}
		if got, ok := rate.Gbps.Get(); (c.gbps != 0) != ok || (ok && got != c.gbps) {
			t.Errorf("%q: gbps %v, want %v", c.raw, rate.Gbps, c.gbps)
		}
		if state := phyState(rate, c.enabled); state != c.state {
			t.Errorf("%q enabled=%v: state %s, want %s", c.raw, c.enabled, state, c.state)
		}
	}
}

// TestSASAddressNormalisation covers the two ways an address can be
// useless: absent, and the null address every unused phy reports.
func TestSASAddressNormalisation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "bare"), "5000CCAB05629D3F", 0o444)
	write(t, filepath.Join(dir, "prefixed"), "0x5000ccab05629d3f", 0o444)
	write(t, filepath.Join(dir, "null"), "0x0000000000000000", 0o444)
	write(t, filepath.Join(dir, "empty"), "", 0o444)
	for _, name := range []string{"bare", "prefixed"} {
		address, ok := readSASAddress(filepath.Join(dir, name))
		if !ok || address != "0x5000ccab05629d3f" {
			t.Errorf("%s: %q %v", name, address, ok)
		}
	}
	for _, name := range []string{"null", "empty", "missing"} {
		if address, ok := readSASAddress(filepath.Join(dir, name)); ok {
			t.Errorf("%s: %q was accepted as an identity", name, address)
		}
	}
}

// TestHostOf covers the two places a host number is read from, because
// getting one wrong silently reports another HBA's links.
func TestHostOf(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]int64{"phy-1:0": 1, "phy-10:0:12": 10, "expander-2:0": 2} {
		if n, ok := hostOfName(raw).Get(); !ok || n != want {
			t.Errorf("hostOfName(%q) = %v, want %d", raw, hostOfName(raw), want)
		}
	}
	for _, raw := range []string{"phy", "phy-x:0", ""} {
		if hostOfName(raw).Present() {
			t.Errorf("hostOfName(%q) invented %v", raw, hostOfName(raw))
		}
	}
	if n, ok := hostOfAddress("11:0:31:0").Get(); !ok || n != 11 {
		t.Errorf("hostOfAddress = %v", hostOfAddress("11:0:31:0"))
	}
	if hostOfAddress("nonsense").Present() {
		t.Error("hostOfAddress accepted a value that is not an address")
	}
}

// TestPHYUnanswered pins the predicate the exporter leaves phys out by
// (ROADMAP 6, fourth hardware run). Each condition is load-bearing: on a
// real shelf 30 disabled phys and 6 with no link answered with their
// counters, and only the 192 vacant ones failed all four.
func TestPHYUnanswered(t *testing.T) {
	t.Parallel()
	refused := ErrorCounters{Failed: 4, Err: Some("4 of the 4 link error counters exist and could not be read")}
	expander := func(state PHYState, counters ErrorCounters) PHY {
		return PHY{Name: "phy-1:0:0", DeviceType: Some("edge expander"), State: state, Counters: counters}
	}
	for _, tc := range []struct {
		name string
		phy  PHY
		want bool
	}{
		{"expander phy, no rate, every counter refused", expander(PHYStateUnknown, refused), true},
		{"fanout expanders count too", PHY{DeviceType: Some("fanout expander"), State: PHYStateUnknown, Counters: refused}, true},
		// A link that is up has been described, whatever its counters did.
		{"up, counters refused", expander(PHYStateUp, refused), false},
		// Disabled is a diagnosis; the phy exists.
		{"disabled, counters refused", expander(PHYStateDisabled, refused), false},
		{"some counters refused", expander(PHYStateUnknown, ErrorCounters{Failed: 3, LossOfDwordSync: Some(int64(0))}), false},
		{"no counters exposed", expander(PHYStateUnknown, ErrorCounters{Err: Some("not exposed at all")}), false},
		{"counters answered", expander(PHYStateUnknown, ErrorCounters{InvalidDword: Some(int64(0))}), false},
		// Host phy counters come from the HBA; all four failing there is a
		// finding, not a vacant phy.
		{"host phy", PHY{DeviceType: Some("end device"), State: PHYStateUnknown, Counters: refused}, false},
		{"no device type", PHY{State: PHYStateUnknown, Counters: refused}, false},
	} {
		if got := tc.phy.Unanswered(); got != tc.want {
			t.Errorf("%s: unanswered = %v, want %v", tc.name, got, tc.want)
		}
	}
}
