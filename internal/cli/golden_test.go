package cli

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

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
				Slot: "1:0:0:0", Device: "/dev/sg0",
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
		fans: []jbod.Fan{
			{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Comment: jbod.Some("low speed"), Speed: 1200},
			{Slot: "1:0:0:0", Description: "Fan B", Index: "2,1", Speed: 3000},
			{Slot: "10:0:0:0", Description: "Cooling fan 1", Index: "2,0", Comment: jbod.Some("normal"), Speed: 4800},
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
