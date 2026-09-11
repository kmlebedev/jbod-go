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
// exposition format shows up as a diff instead of surviving a
// strings.Contains assertion (G).
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
			{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Comment: jbod.Some("low speed"), Speed: 1200},
			{Slot: "1:0:0:0", Description: "Fan B", Index: "2,1", Speed: 3000},
		},
		Errors:   map[string]int{jbod.CollectorFans: 1, jbod.CollectorDisks: 2},
		Duration: 1234 * time.Millisecond,
		Up:       true,
	}
}

func TestEncodeGolden(t *testing.T) {
	t.Parallel()
	golden(t, "metrics.golden", Encode(fullSnapshot(), map[string]int{
		jbod.CollectorDisks: 7,
		jbod.CollectorFans:  3,
	}))
}

func TestEncodeGoldenIncomplete(t *testing.T) {
	t.Parallel()
	s := fullSnapshot()
	s.Up = false
	s.Duration = 120 * time.Second
	golden(t, "metrics-down.golden", Encode(s, s.Errors))
}
