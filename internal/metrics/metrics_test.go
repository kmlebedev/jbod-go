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
	got := Encode(snapshot(), map[string]int{jbod.CollectorFans: 3})
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
	got := Encode(s, s.Errors)
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
	got := Encode(s, nil)
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
	got := Encode(jbod.Snapshot{Up: true}, map[string]int{"psu": 2, jbod.CollectorDisks: 1})
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
