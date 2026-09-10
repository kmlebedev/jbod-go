package jbod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestNatCompare(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int // -1 a<b, 0 equal, 1 a>b
	}{
		{"Slot 2", "Slot 10", -1},
		{"Slot 10", "Slot 2", 1},
		{"Slot 01", "Slot 2", -1},
		{"Slot 9", "Slot 9", 0},
		{"1:0:0:0", "10:0:0:0", -1},
		{"10:0:0:0", "2:0:0:0", 1},
		{"1:0:0:0", "1:0:1:0", -1},
		{"Bay 3", "Slot 1", -1},
		{"Slot 1", "Slot 1, front", -1},
		{"", "Slot 1", -1},
		{"Slot 007", "Slot 7", -1}, // numerically equal: stable byte-order tiebreak
	}
	for _, c := range cases {
		got := natCompare(c.a, c.b)
		if sign(got) != c.want {
			t.Errorf("natCompare(%q, %q) = %d, want sign %d", c.a, c.b, got, c.want)
		}
		if back := natCompare(c.b, c.a); sign(back) != -c.want {
			t.Errorf("natCompare(%q, %q) = %d, not antisymmetric", c.b, c.a, back)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// multiSlot builds a sysfs tree with two enclosures whose slot names sort
// differently as text than as numbers.
func multiSlot(t *testing.T) *Client {
	t.Helper()
	root := t.TempDir()
	layout := map[string][]string{
		"2:0:0:0":  {"Slot 1, front", "Slot 2, front", "Slot 10, front"},
		"10:0:0:0": {"Slot 3, front"},
	}
	n := 0
	for encl, slots := range layout {
		for _, slot := range slots {
			n++
			dir := filepath.Join(root, encl, slot, "device", "scsi_generic", fmt.Sprintf("sg%d", n))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return &Client{Sysfs: root, Run: func(_ context.Context, name string, _ ...string) (string, error) {
		switch name {
		case "lsscsi":
			// Deliberately listed in the "wrong" order.
			return "[10:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n[2:0:0:0] enclosu ACME Shelf 1 - /dev/sg100\n", nil
		case "sg_inq":
			return "Vendor identification: ACME\n", nil
		case "sg_map":
			return "", nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	}}
}

func TestDisksNaturalOrder(t *testing.T) {
	t.Parallel()
	c := multiSlot(t)
	ctx := context.Background()
	es, err := c.Enclosures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := c.Disks(ctx, es, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range ds {
		got = append(got, d.Enclosure+"/"+d.Slot)
	}
	want := []string{
		"2:0:0:0/Slot 1",
		"2:0:0:0/Slot 2",
		"2:0:0:0/Slot 10",
		"10:0:0:0/Slot 3",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: got %q, want %q (full order %v)", i, got[i], want[i], got)
		}
	}
}

// SetLED must accept both the generic device and its /dev/sd* mapping.
func TestSetLEDMatchesDeviceAndMap(t *testing.T) {
	t.Parallel()
	c := fixture(t)
	es, err := c.Enclosures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ds, err := c.Disks(context.Background(), es, false)
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Device != "/dev/sg1" || ds[0].Map != "/dev/sda" {
		t.Fatalf("unexpected fixture disk %+v", ds[0])
	}
	for _, device := range []string{"/dev/sg1", "/dev/sda"} {
		if err := SetLED(ds, device, "fault", true); err != nil {
			t.Fatalf("%s: %v", device, err)
		}
		b, err := os.ReadFile(ds[0].Fault)
		if err != nil || string(b) != "1" {
			t.Fatalf("%s: %q %v", device, b, err)
		}
		if err := SetLED(ds, device, "fault", false); err != nil {
			t.Fatalf("%s: %v", device, err)
		}
	}
	if SetLED(ds, "/dev/sdz", "locate", true) == nil {
		t.Fatal("unmapped device accepted")
	}
	if SetLED(ds, "/dev/sg1", "blink", true) == nil {
		t.Fatal("unknown LED kind accepted")
	}
}
