package metrics

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// update rewrites the golden files: go test ./internal/metrics/ -update.
var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/<name>, so an accidental change to the
// exposition — including one that comes from a client_golang upgrade — shows
// up as a diff instead of surviving a strings.Contains assertion (G).
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/metrics/ -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s does not match.\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// fullSnapshot is a two-shelf scrape with everything the encoder can render:
// a missing temperature, duplicate labels, a fan without a condition and a
// partial collection.
func fullSnapshot() jbod.Snapshot {
	return jbod.Snapshot{
		Enclosures: []jbod.Enclosure{
			{Slot: "1:0:0:0", Device: "/dev/sg0", Vendor: jbod.Some("ACME"), Model: jbod.Some("Shelf"), Serial: jbod.Some("ENC1")},
			{Slot: "10:0:0:0", Device: "/dev/sg9"},
		},
		Disks: []jbod.Disk{
			{Enclosure: "1:0:0:0", Slot: "Slot 01", Temperature: jbod.Some(int64(37))},
			{Enclosure: "1:0:0:0", Slot: "Slot 02"},
			{Enclosure: "1:0:0:0", Slot: "Slot 10", Temperature: jbod.Some(int64(-2))},
			{Enclosure: "10:0:0:0", Slot: "Slot 03", Temperature: jbod.Some(int64(41))},
		},
		Fans: []jbod.Fan{
			{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Comment: jbod.Some("low speed"), Speed: jbod.Some(int64(1200))},
			{Slot: "1:0:0:0", Description: "Fan B", Index: "2,1", Speed: jbod.Some(int64(3000))},
		},
		Status:   shelfStatus(),
		PHYs:     shelfPHYs(),
		Errors:   map[string]int{jbod.CollectorFans: 1, jbod.CollectorDisks: 2},
		Duration: 1234 * time.Millisecond,
		ReadAt:   readAt,
		Up:       true,
	}
}

// shelfPHYs is the SAS transport half of the snapshot: a link that is up
// with clean counters, a link that is up and counting errors, a phy the
// transport described without any counter at all, an expander phy with no
// link that still answered with its counters, and one the expander declined
// to describe (ROADMAP 6).
//
// The third one gets an info series and no counter series, because a zero
// would be a claim that the link is clean and nobody made it. The fourth is
// "unknown" in the state set and keeps its counters. The fifth is the sysfs
// face of a vacant phy: no per-phy series at all, one in its expander's
// count.
func shelfPHYs() []jbod.PHY {
	clean := jbod.ErrorCounters{
		Source: "sysfs", ReadAt: readAt,
		InvalidDword: jbod.Some(int64(0)), RunningDisparityError: jbod.Some(int64(0)),
		LossOfDwordSync: jbod.Some(int64(0)), PhyResetProblem: jbod.Some(int64(0)),
	}
	noisy := jbod.ErrorCounters{
		Source: "sysfs", ReadAt: readAt,
		InvalidDword: jbod.Some(int64(1274)), RunningDisparityError: jbod.Some(int64(7)),
		LossOfDwordSync: jbod.Some(int64(31)), PhyResetProblem: jbod.Some(int64(2)),
	}
	return []jbod.PHY{
		{
			Name: "phy-1:0", Host: jbod.Some(int64(1)), Port: jbod.Some("port-1:0"),
			SASAddress: jbod.Some("0x500605b00b1e2f40"), DeviceType: jbod.Some("end device"),
			Identifier: jbod.Some(int64(0)), State: jbod.PHYStateUp,
			Negotiated: jbod.LinkRate{Text: jbod.Some("12.0 Gbit"), Gbps: jbod.Some(12.0)},
			Counters:   clean,
		},
		{
			Name: "phy-1:1", Host: jbod.Some(int64(1)), Port: jbod.Some("port-1:0"),
			SASAddress: jbod.Some("0x500605b00b1e2f41"), DeviceType: jbod.Some("end device"),
			Identifier: jbod.Some(int64(1)), State: jbod.PHYStateUp,
			Negotiated: jbod.LinkRate{Text: jbod.Some("6.0 Gbit"), Gbps: jbod.Some(6.0)},
			Counters:   noisy,
		},
		{
			Name: "phy-1:0:0", Host: jbod.Some(int64(1)),
			SASAddress: jbod.Some("0x5000ccab05629d3f"), DeviceType: jbod.Some("edge expander"),
			Identifier: jbod.Some(int64(0)), State: jbod.PHYStateDisabled,
			Negotiated: jbod.LinkRate{Text: jbod.Some("Phy disabled")},
			Counters: jbod.ErrorCounters{
				Source: "sysfs", ReadAt: readAt,
				Err: jbod.Some("this phy exposes no link error counters"),
			},
		},
		{
			Name: "phy-1:0:1", Host: jbod.Some(int64(1)),
			SASAddress: jbod.Some("0x5000ccab05629d3f"), DeviceType: jbod.Some("edge expander"),
			Identifier: jbod.Some(int64(1)), State: jbod.PHYStateUnknown,
			Negotiated: jbod.LinkRate{Text: jbod.Some("Unknown")},
			Counters:   clean,
		},
		{
			Name: "phy-1:0:2", Host: jbod.Some(int64(1)),
			SASAddress: jbod.Some("0x5000ccab05629d3f"), DeviceType: jbod.Some("edge expander"),
			Identifier: jbod.Some(int64(2)), State: jbod.PHYStateUnknown,
			Negotiated: jbod.LinkRate{Text: jbod.Some("Unknown")},
			Counters: jbod.ErrorCounters{
				Source: "sysfs", ReadAt: readAt, Failed: 4,
				Err: jbod.Some("4 of the 4 link error counters exist and could not be read"),
			},
		},
	}
}

// readAt is fixed so the snapshot timestamp is a golden value rather than
// the time the test ran.
var readAt = time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)

// shelfStatus is one shelf that answered and one that did not, which is
// what the health series have to keep apart: an enclosure in trouble and an
// enclosure nobody could read are different alerts (ROADMAP 5).
func shelfStatus() []jbod.EnclosureStatus {
	full := jbod.EnclosureStatus{
		Enclosure: "1:0:0:0", EnclosureID: jbod.Some("ENC1"), Address: "ENC1", StableID: true,
		Hardware: jbod.HardwareStatus{
			Level:       jbod.HealthWarning,
			NonCritical: jbod.Some(true), Critical: jbod.Some(false),
		},
		Components: []jbod.Component{
			{
				Enclosure: "1:0:0:0", Index: "0,0", Type: "array device slot", Name: "SLOT 00",
				Status: jbod.Some("OK"), Health: jbod.HealthOK, SlotNumber: jbod.Some(int64(0)),
				SASAddresses: []string{"0x5000cca2a0d6e2f5"},
				Device:       jbod.Some("/dev/sg1"), Map: jbod.Some("/dev/sda"),
			},
			{
				// A bay the near module cannot reach: no address, so no
				// mapping series, and unknown is not ok.
				Enclosure: "1:0:0:0", Index: "0,1", Type: "array device slot", Name: "SLOT 01",
				Status: jbod.Some("No access allowed"), Health: jbod.HealthUnknown,
			},
			{
				Enclosure: "1:0:0:0", Index: "1,0", Type: "power supply", Name: "PSU A",
				Status: jbod.Some("Critical"), Health: jbod.HealthCritical,
			},
			{
				Enclosure: "1:0:0:0", Index: "3,0", Type: "temperature sensor", Name: "TEMP A",
				Status: jbod.Some("OK"), Health: jbod.HealthOK,
				Readings: []jbod.Reading{{
					Kind: jbod.ReadingTemperature, Unit: jbod.UnitCelsius, Value: jbod.Some(35.0),
					Source: "sg_ses --join", ReadAt: readAt,
					Thresholds: &jbod.Thresholds{
						HighCritical: jbod.Some(65.0), HighWarning: jbod.Some(60.0),
						LowWarning: jbod.Some(0.0), LowCritical: jbod.Some(-19.0),
					},
				}},
			},
			{
				// A sensor that reported nothing. It gets no value series
				// at all: a zero here is a cold disk that is not cold.
				Enclosure: "1:0:0:0", Index: "3,1", Type: "temperature sensor", Name: "TEMP B",
				Status: jbod.Some("Unsupported"), Health: jbod.HealthUnknown,
				Readings: []jbod.Reading{{
					Kind: jbod.ReadingTemperature, Unit: jbod.UnitCelsius,
					Source: "sg_ses --join", ReadAt: readAt,
					Err: jbod.Some("the element declares this reading but reported no value"),
				}},
			},
			{
				Enclosure: "1:0:0:0", Index: "4,0", Type: "voltage sensor", Name: "VOLT 12V",
				Status: jbod.Some("OK"), Health: jbod.HealthOK,
				Readings: []jbod.Reading{{
					Kind: jbod.ReadingVoltage, Unit: jbod.UnitVolts, Value: jbod.Some(12.01),
					Source: "sg_ses --join", ReadAt: readAt,
				}},
			},
		},
		Summary: jbod.ComponentSummary{
			Total: 6, Level: jbod.HealthCritical,
			Counts: map[jbod.HealthLevel]int{jbod.HealthOK: 3, jbod.HealthCritical: 1, jbod.HealthUnknown: 2},
		},
		Collection: jbod.CollectionStatus{
			Complete: true, ReadAt: readAt, Generation: jbod.Some("0x1"), Missing: 1,
			Pages: []jbod.PageStatus{
				{Name: "configuration", OK: true, Required: true},
				{Name: "join", OK: true, Required: true},
				{Name: "enclosure status", OK: true, Required: true},
				{Name: "threshold in", OK: false, Required: false, Err: jbod.Some("not supported")},
			},
		},
	}
	unreadable := jbod.EnclosureStatus{
		Enclosure: "10:0:0:0", Address: "10:0:0:0",
		Hardware: jbod.HardwareStatus{Level: jbod.HealthUnknown, Err: jbod.Some("device or resource busy")},
		Summary:  jbod.ComponentSummary{Level: jbod.HealthUnknown, Counts: map[jbod.HealthLevel]int{}},
		Collection: jbod.CollectionStatus{
			Complete: false, ReadAt: readAt, GenerationChanged: true,
			Pages: []jbod.PageStatus{
				{Name: "configuration", OK: false, Required: true, Err: jbod.Some("device or resource busy")},
				{Name: "join", OK: false, Required: true, Err: jbod.Some("device or resource busy")},
				{Name: "enclosure status", OK: false, Required: true, Err: jbod.Some("device or resource busy")},
				{Name: "threshold in", OK: false, Required: false, Err: jbod.Some("device or resource busy")},
			},
		},
	}
	return []jbod.EnclosureStatus{full, unreadable}
}

func TestEncodeGolden(t *testing.T) {
	t.Parallel()
	golden(t, "metrics.golden", encode(t, fullSnapshot(), map[string]int{
		jbod.CollectorDisks: 7,
		jbod.CollectorFans:  3,
	}, Options{Deprecated: true}))
}

func TestEncodeGoldenIncomplete(t *testing.T) {
	t.Parallel()
	s := fullSnapshot()
	s.Up = false
	s.Duration = 120 * time.Second
	golden(t, "metrics-down.golden", encode(t, s, s.Errors, Options{Deprecated: true}))
}
