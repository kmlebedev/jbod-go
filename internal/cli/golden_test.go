package cli

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// update rewrites the golden files: go test ./internal/cli/ -update.
var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares the rendered table with testdata/<name>. strings.Contains
// assertions cannot see a line that appeared or disappeared; a golden file
// can (G).
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
		t.Fatalf("%v (run: go test ./internal/cli/ -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s does not match.\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// shelf is a two-enclosure inventory covering the interesting rendering
// cases: a shelf that did not answer sg_inq, a disk with full telemetry, a
// disk with none, natural slot order and a fan without a condition.
func shelf() *fake {
	return &fake{
		enclosures: []jbod.Enclosure{
			{
				Slot: "1:0:0:0", Device: "/dev/sg0", ID: jbod.Some("0x5000ccab05629d00"),
				Vendor: jbod.Some("ACME"), Model: jbod.Some("Shelf 24"),
				Revision: jbod.Some("1.03"), Serial: jbod.Some("ENC00001"),
			},
			{Slot: "10:0:0:0", Device: "/dev/sg9"},
		},
		disks: []jbod.Disk{
			{
				Enclosure: "1:0:0:0", Slot: "Slot 01", SlotLabel: "Slot 01, front", Device: "/dev/sg1",
				Map: jbod.Some("/dev/sda"), Vendor: jbod.Some("ACME"), Model: jbod.Some("HDD-16T"),
				Serial: jbod.Some("S0000001"), Firmware: jbod.Some("FW01"), Temperature: jbod.Some(int64(37)),
			},
			{Enclosure: "1:0:0:0", Slot: "Slot 02", SlotLabel: "Slot 02, front", Device: "/dev/sg2"},
			{
				Enclosure: "1:0:0:0", Slot: "Slot 10", SlotLabel: "Slot 10, front", Device: "/dev/sg3",
				Map: jbod.Some("/dev/sdc"), Vendor: jbod.Some("ACME"), Model: jbod.Some("HDD-16T"),
				Serial: jbod.Some("S0000003"), Firmware: jbod.Some("FW02"), Temperature: jbod.Some(int64(-2)),
			},
			{
				Enclosure: "10:0:0:0", Slot: "Slot 03", SlotLabel: "Slot 03, rear", Device: "/dev/sg4",
				Temperature: jbod.Some(int64(41)),
			},
		},
		statuses: statuses(),
		fans: []jbod.Fan{
			{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Comment: jbod.Some("low speed"), Speed: jbod.Some(int64(1200))},
			{Slot: "1:0:0:0", Description: "Fan B", Index: "2,1", Speed: jbod.Some(int64(3000))},
			{Slot: "10:0:0:0", Description: "Cooling fan 1", Index: "2,0", Comment: jbod.Some("normal"), Speed: jbod.Some(int64(4800))},
		},
	}
}

func TestListGolden(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		file string
	}{
		{"enclosures", []string{"-e"}, "list-enclosures.golden"},
		{"disks", []string{"-d"}, "list-disks.golden"},
		{"fans", []string{"-f"}, "list-fans.golden"},
		{"everything", []string{"-edf"}, "list-all.golden"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := cmdList(context.Background(), c.args, &out, shelf()); err != nil {
				t.Fatal(err)
			}
			golden(t, c.file, out.String())
		})
	}
}

func TestLEDGolden(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	args := []string{"-l", "/dev/sda", "--locate", "/dev/sdc", "--fault", "/dev/sg4", "--on"}
	if err := cmdLED(context.Background(), args, &out, shelf()); err != nil {
		t.Fatal(err)
	}
	golden(t, "led-on.golden", out.String())
}

// statuses is the inspection fixture behind the health, sensor and
// component reports: one shelf that answered everything except its
// threshold page, and one that could not be read at all.
//
// It covers what the three reports have to keep apart — a critical power
// supply, a bay the near module cannot reach, an element the configuration
// declares and nothing reported, and a sensor with thresholds — because
// each of them renders differently and each of them has been rendered
// wrongly before.
func statuses() []jbod.EnclosureStatus {
	readAt := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	full := jbod.EnclosureStatus{
		Enclosure: "1:0:0:0", EnclosureID: jbod.Some("0x5000ccab05629d00"),
		Address: "0x5000ccab05629d00", StableID: true,
		Hardware: jbod.HardwareStatus{
			Level:            jbod.HealthWarning,
			InvalidOperation: jbod.Some(false), Info: jbod.Some(false),
			NonCritical: jbod.Some(true), Critical: jbod.Some(false), Unrecoverable: jbod.Some(false),
		},
		Components: []jbod.Component{
			{
				Enclosure: "1:0:0:0", Index: "0,0", Type: "array device slot", Name: "SLOT 00",
				Status: jbod.Some("OK"), Health: jbod.HealthOK, SlotNumber: jbod.Some(int64(0)),
				SASAddresses: []string{"0x5000cca2a0d6e2f5"},
				Device:       jbod.Some("/dev/sg1"), Map: jbod.Some("/dev/sda"),
			},
			{
				Enclosure: "1:0:0:0", Index: "0,1", Type: "array device slot", Name: "SLOT 01",
				Status: jbod.Some("No access allowed"), Health: jbod.HealthUnknown,
				SlotNumber: jbod.Some(int64(1)),
			},
			{
				Enclosure: "1:0:0:0", Index: "0,2", Type: "array device slot", Name: "SLOT 02",
				Health: jbod.HealthUnknown, Declared: true,
				Err: jbod.Some("declared by the configuration page and not reported by any status page"),
			},
			{
				Enclosure: "1:0:0:0", Index: "1,0", Type: "power supply", Name: "PSU A",
				Status: jbod.Some("OK"), Health: jbod.HealthOK,
				Readings: []jbod.Reading{{
					Kind: jbod.ReadingTemperature, Unit: jbod.UnitCelsius,
					Value: jbod.Some(41.0), Source: "sg_ses --join", ReadAt: readAt,
				}},
			},
			{
				Enclosure: "1:0:0:0", Index: "1,1", Type: "power supply", Name: "PSU B",
				Status: jbod.Some("Critical"), Health: jbod.HealthCritical,
			},
			{
				Enclosure: "1:0:0:0", Index: "2,0", Type: "cooling", Name: "FAN ENCL 1",
				Status: jbod.Some("OK"), Health: jbod.HealthOK,
				Readings: []jbod.Reading{{
					Kind: jbod.ReadingSpeed, Unit: jbod.UnitRPM,
					Value: jbod.Some(7220.0), Source: "sg_ses --join", ReadAt: readAt,
				}},
			},
			{
				Enclosure: "1:0:0:0", Index: "3,0", Type: "temperature sensor", Name: "TEMP IOM A",
				Status: jbod.Some("OK"), Health: jbod.HealthOK,
				Readings: []jbod.Reading{{
					Kind: jbod.ReadingTemperature, Unit: jbod.UnitCelsius,
					Value: jbod.Some(35.0), Source: "sg_ses --join", ReadAt: readAt,
					Thresholds: &jbod.Thresholds{
						HighCritical: jbod.Some(65.0), HighWarning: jbod.Some(60.0),
						LowWarning: jbod.Some(0.0), LowCritical: jbod.Some(-19.0),
					},
				}},
			},
			{
				// A sensor that declares a reading and reported none: the
				// row stays, the value does not become a zero.
				Enclosure: "1:0:0:0", Index: "3,1", Type: "temperature sensor", Name: "TEMP IOM B",
				Status: jbod.Some("Unsupported"), Health: jbod.HealthUnknown,
				Readings: []jbod.Reading{{
					Kind: jbod.ReadingTemperature, Unit: jbod.UnitCelsius,
					Source: "sg_ses --join", ReadAt: readAt,
					Err: jbod.Some("the element declares this reading but reported no value"),
				}},
			},
		},
		Collection: jbod.CollectionStatus{
			Complete: true, ReadAt: readAt, Generation: jbod.Some("0x1"), Missing: 1,
			Pages: []jbod.PageStatus{
				{Name: "configuration", Command: "sg_ses --page=cf /dev/sg0", OK: true, Required: true, Generation: jbod.Some("0x1")},
				{Name: "join", Command: "sg_ses --join /dev/sg0", OK: true, Required: true},
				{Name: "enclosure status", Command: "sg_ses --page=es /dev/sg0", OK: true, Required: true, Generation: jbod.Some("0x1")},
				{Name: "threshold in", Command: "sg_ses --page=th /dev/sg0", OK: false, Required: false,
					Err: jbod.Some("sg_ses: Threshold In dpage not supported")},
			},
		},
	}
	full.Summary = summaryOf(full.Components)
	// A shelf whose pages did not answer at all: unknown everywhere, and
	// not one healthy component.
	unreadable := jbod.EnclosureStatus{
		Enclosure: "10:0:0:0", Address: "10:0:0:0", StableID: false,
		Hardware: jbod.HardwareStatus{
			Level: jbod.HealthUnknown,
			Err:   jbod.Some("sg_ses: device or resource busy"),
		},
		Summary: jbod.ComponentSummary{Level: jbod.HealthUnknown, Counts: map[jbod.HealthLevel]int{}},
		Collection: jbod.CollectionStatus{
			Complete: false, ReadAt: readAt,
			Pages: []jbod.PageStatus{
				{Name: "configuration", Command: "sg_ses --page=cf /dev/sg9", OK: false, Required: true,
					Err: jbod.Some("sg_ses: device or resource busy")},
				{Name: "join", Command: "sg_ses --join /dev/sg9", OK: false, Required: true,
					Err: jbod.Some("sg_ses: device or resource busy")},
				{Name: "enclosure status", Command: "sg_ses --page=es /dev/sg9", OK: false, Required: true,
					Err: jbod.Some("sg_ses: device or resource busy")},
				{Name: "threshold in", Command: "sg_ses --page=th /dev/sg9", OK: true, Required: false,
					Err: jbod.Some("not read: the shelf reports no sensor elements")},
			},
		},
	}
	return []jbod.EnclosureStatus{full, unreadable}
}

// summaryOf rolls the fixture's components up the way the client does.
func summaryOf(components []jbod.Component) jbod.ComponentSummary {
	summary := jbod.ComponentSummary{Total: len(components), Counts: map[jbod.HealthLevel]int{}}
	worst := jbod.HealthUnknown
	rank := map[jbod.HealthLevel]int{jbod.HealthOK: 0, jbod.HealthWarning: 1, jbod.HealthCritical: 2, jbod.HealthUnrecoverable: 3}
	best := -1
	for _, c := range components {
		summary.Counts[c.Health]++
		if n, ok := rank[c.Health]; ok && n > best {
			worst, best = c.Health, n
		}
	}
	summary.Level = worst
	return summary
}

func TestHealthGolden(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdHealth(context.Background(), nil, &out, shelf()); err != nil {
		t.Fatal(err)
	}
	golden(t, "health.golden", out.String())
}

func TestSensorsGolden(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdSensors(context.Background(), nil, &out, shelf()); err != nil {
		t.Fatal(err)
	}
	golden(t, "sensors.golden", out.String())
}

func TestComponentsGolden(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdList(context.Background(), []string{"--components"}, &out, shelf()); err != nil {
		t.Fatal(err)
	}
	golden(t, "list-components.golden", out.String())
}
