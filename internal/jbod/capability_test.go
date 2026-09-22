package jbod

import (
	"context"
	"strings"
	"testing"
)

// report indexes a capability report by capability name.
func report(t *testing.T, c *Client) (EnclosureCapabilities, map[string]Capability) {
	t.Helper()
	ctx := context.Background()
	// A shelf that did not answer sg_inq is still a shelf, and reporting
	// its capabilities is exactly what a failed probe is for, so the
	// partial error is not fatal here.
	es, err := c.Enclosures(ctx)
	if len(es) == 0 {
		t.Fatalf("no enclosures: %v", err)
	}
	reports, err := c.Capabilities(ctx, es)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(reports))
	}
	index := map[string]Capability{}
	for _, entry := range reports[0].Capabilities {
		index[entry.Name] = entry
	}
	return reports[0], index
}

// TestCapabilitiesReadAndWriteAreJudgedApart is the core of ROADMAP 3: a
// page we can read is not a page we can write, and discovery may not find
// out by trying.
func TestCapabilitiesReadAndWriteAreJudgedApart(t *testing.T) {
	t.Parallel()
	summary, caps := report(t, bays(t))

	if summary.Components != 6 || !summary.StableID || summary.Address != "naa.50050cc10c400000" {
		t.Fatalf("unexpected header: %+v", summary)
	}

	cases := []struct {
		name  string
		read  Support
		write Support
	}{
		// Enumeration is a directory walk: readable, and there is nothing
		// to write.
		{"slot.enumeration", SupportSupported, SupportUnsupported},
		// The logical identifier is a read-only file.
		{"enclosure.id", SupportSupported, SupportUnsupported},
		// type and slot have no store handler, so the kernel creates them
		// read-only and the verdict follows the mode bits.
		{"slot.type", SupportSupported, SupportUnsupported},
		{"slot.number", SupportSupported, SupportUnsupported},
		// The indicators are writable, which is as far as discovery can
		// honestly go: never "supported".
		{"led.locate", SupportSupported, SupportUnknown},
		{"led.fault", SupportSupported, SupportUnknown},
		{"slot.power_status", SupportSupported, SupportUnknown},
		// sg_inq answered during discovery, so this one is a fact.
		{"enclosure.identity", SupportSupported, SupportUnsupported},
		{"fan.rpm", SupportSupported, SupportUnsupported},
	}
	for _, tc := range cases {
		entry, ok := caps[tc.name]
		if !ok {
			t.Errorf("%s is missing from the report", tc.name)
			continue
		}
		if entry.Read != tc.read || entry.Write != tc.write {
			t.Errorf("%s: read=%s write=%s, want read=%s write=%s (%s)",
				tc.name, entry.Read, entry.Write, tc.read, tc.write, entry.Evidence)
		}
		if entry.Evidence == "" {
			t.Errorf("%s: no evidence", tc.name)
		}
	}

	// No capability may claim a supported write from discovery alone: only
	// a readback after a real write can raise that (ROADMAP 3).
	for name, entry := range caps {
		if entry.Write == SupportSupported {
			t.Errorf("%s claims a proven write from discovery: %s", name, entry.Evidence)
		}
	}
	// And the evidence says why.
	if !strings.Contains(caps["led.locate"].Evidence, "readback") {
		t.Errorf("led.locate does not explain the unknown write: %s", caps["led.locate"].Evidence)
	}
}

// TestCapabilitiesReportMissingAttributesAsUnsupported separates "the shelf
// does not have this" from "we could not find out".
func TestCapabilitiesReportMissingAttributesAsUnsupported(t *testing.T) {
	t.Parallel()
	// A read-only locate attribute is the kernel's way of saying the
	// driver has no store handler: the write is unsupported, not unknown.
	_, caps := report(t, bays(t, withReadOnlyLED("Slot 01, front")))
	if got := caps["led.locate"]; got.Read != SupportSupported {
		t.Errorf("led.locate read=%s, want supported", got.Read)
	}
	// Five of the six slots still expose a writable locate, so the shelf
	// as a whole stays unknown; the evidence carries the count.
	if !strings.Contains(caps["led.locate"].Evidence, "components expose locate") {
		t.Errorf("evidence lost the per-component counts: %s", caps["led.locate"].Evidence)
	}

	// A tool that is not installed is unsupported with the failure kept
	// apart from the verdict; an injected runner cannot know either way.
	if got := caps["disk.temperature"]; got.Read != SupportUnknown {
		t.Errorf("disk.temperature read=%s with an injected runner, want unknown (%s)", got.Read, got.Evidence)
	}
}

// TestCapabilitiesKeepProbeFailuresApartFromUnsupported covers the rule
// that a transport error is not an answer about the hardware.
func TestCapabilitiesKeepProbeFailuresApartFromUnsupported(t *testing.T) {
	t.Parallel()
	c := bays(t).With(WithRunner(func(_ context.Context, name string, _ ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n", nil
		case "sg_map":
			return "", nil
		}
		// sg_inq and sg_ses fail the way a busy device does.
		return "", context.DeadlineExceeded
	}))
	_, caps := report(t, c)
	for _, name := range []string{"enclosure.identity", "fan.rpm"} {
		entry := caps[name]
		if entry.Read != SupportUnknown {
			t.Errorf("%s: read=%s after a failed probe, want unknown", name, entry.Read)
		}
		if !entry.Err.Present() {
			t.Errorf("%s: the probe failure was not reported", name)
		}
	}
	// A shelf whose element listing is empty, as opposed to failing, does
	// report unsupported.
	quiet := bays(t).With(WithRunner(func(_ context.Context, name string, _ ...string) (string, error) {
		if name == "lsscsi" {
			return "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n", nil
		}
		return "", nil
	}))
	_, caps = report(t, quiet)
	if entry := caps["fan.rpm"]; entry.Read != SupportUnsupported || entry.Err.Present() {
		t.Errorf("an empty element listing: read=%s err=%v", entry.Read, entry.Err)
	}
}

// TestCapabilitiesDoNotWrite is the discovery rule stated as a test: the
// probe must not touch a single attribute.
func TestCapabilitiesDoNotWrite(t *testing.T) {
	t.Parallel()
	c := bays(t).With(WithLEDWriter(func(path, value string) error {
		t.Errorf("capability discovery wrote %q to %s", value, path)
		return nil
	}))
	if _, caps := report(t, c); len(caps) == 0 {
		t.Fatal("no capabilities reported")
	}
}

// TestSupportedChecksNamedCapabilities covers the helper commands use
// before promising output they cannot produce.
func TestSupportedChecksNamedCapabilities(t *testing.T) {
	t.Parallel()
	summary, _ := report(t, bays(t))
	if !summary.Supported("slot.enumeration", "led.locate") {
		t.Error("Supported() denied capabilities the report lists as readable")
	}
	if summary.Supported("slot.enumeration", "disk.temperature") {
		t.Error("Supported() accepted a capability that is only unknown")
	}
	if summary.Supported("no.such.capability") {
		t.Error("Supported() accepted a name that is not in the report")
	}
}

// TestSASCapabilities covers the capability report of the transport half of
// v1.3 (ROADMAP 6).
//
// It is reported per shelf and it is about the HBA the shelf is attached
// through, which is why the evidence names the host: a phy belongs to the
// host, and a report that implied it belonged to the enclosure would be
// claiming a topology nobody established.
func TestSASCapabilities(t *testing.T) {
	t.Parallel()
	_, caps := report(t, bays(t, withSASClass(links(t).sysClass)))
	phy := caps["sas.phy"]
	if phy.Read != SupportSupported || phy.Write != SupportUnsupported {
		t.Errorf("sas.phy %s", phy)
	}
	if !strings.Contains(phy.Evidence, "host 1") {
		t.Errorf("the evidence does not name the host: %s", phy.Evidence)
	}
	// One of the five phys of host 1 publishes no counters and one has no
	// attributes at all, so the counters are readable on some and not on
	// others: that is unknown, not supported and not unsupported.
	counters := caps["sas.phy_error_counters"]
	if counters.Read != SupportUnknown || counters.Write != SupportUnsupported {
		t.Errorf("sas.phy_error_counters %s", counters)
	}
	// smp_utils is not installed in this fixture's PATH, and the client
	// runs an injected runner, so whether it is there is not knowable.
	smp := caps["smp.phy_error_counters"]
	if smp.Read == SupportSupported {
		t.Errorf("SMP support was claimed without sending a request: %s", smp)
	}
	if smp.Write != SupportUnsupported {
		t.Errorf("smp.phy_error_counters must never report a write: %s", smp)
	}
}

// TestSASCapabilitiesWithoutTransport covers the common shelf that is not
// behind SAS at all: unsupported, with a reason, and not an error.
func TestSASCapabilitiesWithoutTransport(t *testing.T) {
	t.Parallel()
	_, caps := report(t, bays(t))
	for _, name := range []string{"sas.phy", "sas.phy_error_counters", "smp.phy_error_counters"} {
		entry := caps[name]
		if entry.Read != SupportUnsupported {
			t.Errorf("%s: read %s, want unsupported", name, entry.Read)
		}
		if entry.Evidence == "" {
			t.Errorf("%s: no evidence", name)
		}
		if entry.Err.Present() {
			t.Errorf("%s: a missing transport is not a probe failure: %s", name, entry.Err)
		}
	}
}
