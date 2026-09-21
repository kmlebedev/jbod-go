package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// snapshot returns a snapshot with one shelf, three disks (one without a
// temperature, two sharing a label) and two fans.
func snapshot() jbod.Snapshot {
	return jbod.Snapshot{
		Enclosures: []jbod.Enclosure{{Slot: "1:0:0:0", Device: "/dev/sg0"}},
		Disks: []jbod.Disk{
			{Enclosure: "1:0:0:0", Slot: "Slot 01", Temperature: jbod.Some(int64(37))},
			{Enclosure: "1:0:0:0", Slot: "Slot 02"},
			{Enclosure: "1:0:0:0", Slot: "Slot 01", Temperature: jbod.Some(int64(41))},
		},
		Fans: []jbod.Fan{
			{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Speed: 1200},
			{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Speed: 1500},
		},
		Errors:   map[string]int{jbod.CollectorFans: 1},
		Duration: 1234 * time.Millisecond,
		Up:       true,
	}
}

func TestEncode(t *testing.T) {
	t.Parallel()
	got := Encode(snapshot(), map[string]int{jbod.CollectorFans: 3}, Options{Deprecated: true})
	for _, want := range []string{
		"# TYPE number_of_enclosures gauge\nnumber_of_enclosures 1\n",
		// A disk without a reading is skipped, not exported as zero.
		`jbod_slot_temperature{slot="Slot 01",enclosure="1:0:0:0"} 41`,
		"# TYPE jbod_up gauge\njbod_up 1\n",
		"jbod_scrape_duration_seconds 1.234\n",
		"# TYPE jbod_scrape_errors_total counter\n",
		`jbod_scrape_errors_total{collector="enclosures"} 0`,
		`jbod_scrape_errors_total{collector="disks"} 0`,
		// The counter comes from the running totals, not from this pass.
		`jbod_scrape_errors_total{collector="fans"} 3`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, `slot="Slot 02"`) {
		t.Errorf("absent temperature exported:\n%s", got)
	}
	// Duplicate label sets collapse to one series, last value winning, as
	// the original gauge vector did.
	if n := strings.Count(got, `jbod_slot_temperature{slot="Slot 01"`); n != 1 {
		t.Errorf("%d series for one label set:\n%s", n, got)
	}
	if !strings.Contains(got, `jbod_fan_rpm{device="Fan A",slot="2,0"} 1500`) || strings.Count(got, "jbod_fan_rpm{") != 1 {
		t.Errorf("fan series not collapsed:\n%s", got)
	}
}

func TestEncodeIncompleteCollection(t *testing.T) {
	t.Parallel()
	s := snapshot()
	s.Up = false
	got := Encode(s, s.Errors, Options{Deprecated: true})
	if !strings.Contains(got, "jbod_up 0") {
		t.Errorf("an incomplete pass must report jbod_up 0:\n%s", got)
	}
	// Even then the readings that were collected are exported.
	if !strings.Contains(got, "jbod_slot_temperature{") {
		t.Errorf("partial data dropped:\n%s", got)
	}
}

func TestEncodeEscapesLabels(t *testing.T) {
	t.Parallel()
	s := jbod.Snapshot{
		Disks: []jbod.Disk{{Enclosure: "enc\n1", Slot: `Slot "1"\x`, Temperature: jbod.Some(int64(20))}},
		Fans:  []jbod.Fan{{Description: "Fan\\A", Index: `2,"0"`, Speed: 900}},
		Up:    true,
	}
	got := Encode(s, nil, Options{Deprecated: true})
	for _, want := range []string{
		`jbod_slot_temperature{slot="Slot \"1\"\\x",enclosure="enc\n1"} 20`,
		`jbod_fan_rpm{device="Fan\\A",slot="2,\"0\""} 900`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if got := label("a\\b\"c\nd"); got != `a\\b\"c\nd` {
		t.Errorf("label = %q", got)
	}
}

// TestEncodeUnknownCollector keeps a new collector from silently dropping out
// of the output if someone adds one without touching this package.
func TestEncodeUnknownCollector(t *testing.T) {
	t.Parallel()
	got := Encode(jbod.Snapshot{Up: true}, map[string]int{"psu": 2, jbod.CollectorDisks: 1}, Options{Deprecated: true})
	if !strings.Contains(got, `jbod_scrape_errors_total{collector="psu"} 2`) {
		t.Errorf("unknown collector missing:\n%s", got)
	}
	// The known collectors keep their order and come first.
	first := strings.Index(got, `collector="enclosures"`)
	last := strings.Index(got, `collector="psu"`)
	if first < 0 || last < first {
		t.Errorf("collector order:\n%s", got)
	}
}

// TestFanSeriesDoNotCollideAcrossEnclosures is the readiness criterion of
// ROADMAP 4: two shelves with identically named fans are two fans.
//
// jbod_fan_rpm labels a cooling element with its description and its SES
// index only, and both are per-shelf, so "Fan A" at index 2,0 collides with
// every other shelf in the rack and the last one written wins. The fix
// cannot be made in place without changing what the existing series means,
// so it is a new name that carries the enclosure.
func TestFanSeriesDoNotCollideAcrossEnclosures(t *testing.T) {
	t.Parallel()
	s := jbod.Snapshot{
		Enclosures: []jbod.Enclosure{
			{Slot: "1:0:0:0", Device: "/dev/sg0", ID: jbod.Some("naa.5000000000000001")},
			{Slot: "10:0:0:0", Device: "/dev/sg9", ID: jbod.Some("naa.5000000000000002")},
		},
		Fans: []jbod.Fan{
			{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Speed: 1200},
			{Slot: "10:0:0:0", Description: "Fan A", Index: "2,0", Speed: 4800},
		},
		Up: true,
	}
	got := Encode(s, nil, Options{Deprecated: true})

	for _, want := range []string{
		`jbod_fan_speed_rpm{enclosure="1:0:0:0",enclosure_id="naa.5000000000000001",component="Fan A",component_id="2,0"} 1200`,
		`jbod_fan_speed_rpm{enclosure="10:0:0:0",enclosure_id="naa.5000000000000002",component="Fan A",component_id="2,0"} 4800`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "jbod_fan_speed_rpm{"); n != 2 {
		t.Errorf("got %d corrected fan series, want 2:\n%s", n, got)
	}
	// The old series still collides, which is why it is deprecated rather
	// than quietly relabelled: relabelling it would change the meaning of
	// a series that dashboards already read.
	if n := strings.Count(got, "jbod_fan_rpm{"); n != 1 {
		t.Errorf("got %d deprecated fan series, want the single colliding one:\n%s", n, got)
	}
	if !strings.Contains(got, "# HELP jbod_fan_rpm DEPRECATED") {
		t.Error("the deprecated series is not marked as such")
	}

	// And it can be switched off once nothing reads it.
	without := Encode(s, nil, Options{})
	if strings.Contains(without, "jbod_fan_rpm{") {
		t.Error("the deprecated series survived Options{Deprecated: false}")
	}
	if n := strings.Count(without, "jbod_fan_speed_rpm{"); n != 2 {
		t.Errorf("the corrected series must stay: %s", without)
	}
}

// TestEnclosureInfoCarriesTheIdentity covers the join target for every
// series labelled with the SCSI address, and the marker that says when that
// identity is really just the address again (ROADMAP 3).
func TestEnclosureInfoCarriesTheIdentity(t *testing.T) {
	t.Parallel()
	s := jbod.Snapshot{
		Enclosures: []jbod.Enclosure{
			{Slot: "1:0:0:0", ID: jbod.Some("naa.5000"), Vendor: jbod.Some("ACME"), Model: jbod.Some("Shelf 24")},
			{Slot: "2:0:0:0", Serial: jbod.Some("ENC2")},
			{Slot: "3:0:0:0"},
		},
		Up: true,
	}
	got := Encode(s, nil, Options{})
	for _, want := range []string{
		`jbod_enclosure_info{enclosure="1:0:0:0",enclosure_id="naa.5000",id_source="logical",vendor="ACME",model="Shelf 24",revision="",serial=""} 1`,
		`jbod_enclosure_info{enclosure="2:0:0:0",enclosure_id="ENC2",id_source="serial",vendor="",model="",revision="",serial="ENC2"} 1`,
		`jbod_enclosure_info{enclosure="3:0:0:0",enclosure_id="3:0:0:0",id_source="address",vendor="",model="",revision="",serial=""} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

// TestSlotCountsPerOccupancy covers the three states reaching the metrics,
// and the series existing at zero so a state going empty is visible.
func TestSlotCountsPerOccupancy(t *testing.T) {
	t.Parallel()
	s := jbod.Snapshot{
		Enclosures: []jbod.Enclosure{{Slot: "1:0:0:0", ID: jbod.Some("naa.5000")}},
		Slots: []jbod.Slot{
			{Enclosure: "1:0:0:0", Label: "Slot 01", Occupancy: jbod.OccupancyOccupied},
			{Enclosure: "1:0:0:0", Label: "Slot 02", Occupancy: jbod.OccupancyOccupied},
			{Enclosure: "1:0:0:0", Label: "Slot 03", Occupancy: jbod.OccupancyEmpty},
			{Enclosure: "1:0:0:0", Label: "Slot 04", Occupancy: jbod.OccupancyUnavailable},
		},
		Up: true,
	}
	got := Encode(s, nil, Options{})
	for _, want := range []string{
		`jbod_enclosure_slots{enclosure="1:0:0:0",enclosure_id="naa.5000",occupancy="occupied"} 2`,
		`jbod_enclosure_slots{enclosure="1:0:0:0",enclosure_id="naa.5000",occupancy="empty"} 1`,
		`jbod_enclosure_slots{enclosure="1:0:0:0",enclosure_id="naa.5000",occupancy="unavailable"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	// A shelf with no slots at all still publishes the three series, so a
	// count dropping to zero is a value and not a gap.
	empty := Encode(jbod.Snapshot{Enclosures: s.Enclosures, Up: true}, nil, Options{})
	if n := strings.Count(empty, "jbod_enclosure_slots{"); n != 3 {
		t.Errorf("got %d occupancy series for an empty shelf, want 3:\n%s", n, empty)
	}
}
