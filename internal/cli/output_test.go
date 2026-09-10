package cli

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

var columns = regexp.MustCompile(` +`)

// collapse squeezes the tabwriter padding, so these tests assert on the
// content of a row and not on column widths that shift with the data.
func collapse(s string) string { return columns.ReplaceAllString(s, " ") }

// fake answers from fixed data, so these tests exercise the rendering only:
// no lsscsi output to parse and no sysfs tree to build (C5).
type fake struct {
	enclosures []jbod.Enclosure
	disks      []jbod.Disk
	fans       []jbod.Fan
	err        error
	leds       []ledCall
}

type ledCall struct {
	device string
	kind   jbod.LEDKind
	on     bool
}

func (f *fake) Enclosures(context.Context) ([]jbod.Enclosure, error) {
	return f.enclosures, f.err
}

func (f *fake) Disks(context.Context, []jbod.Enclosure, jbod.DiskOptions) ([]jbod.Disk, error) {
	return f.disks, f.err
}

func (f *fake) Fans(context.Context, []jbod.Enclosure) ([]jbod.Fan, error) {
	return f.fans, f.err
}

func (f *fake) SetLED(_ context.Context, device string, kind jbod.LEDKind, on bool) error {
	f.leds = append(f.leds, ledCall{device: device, kind: kind, on: on})
	return f.err
}

// TestListRendersAbsentReadings pins the sentinels to the output layer: the
// collectors now report absence, and only the CLI turns it into ERR/N/A/NONE
// (C1).
func TestListRendersAbsentReadings(t *testing.T) {
	t.Parallel()
	inv := &fake{
		enclosures: []jbod.Enclosure{
			{Slot: "1:0:0:0", Device: "/dev/sg0", Vendor: jbod.Some("ACME"), Model: jbod.Some("Shelf"), Revision: jbod.Some("1"), Serial: jbod.Some("ENC1")},
			// A shelf that did not answer sg_inq.
			{Slot: "2:0:0:0", Device: "/dev/sg9"},
		},
		disks: []jbod.Disk{
			{
				Enclosure: "1:0:0:0", Slot: "Slot 01", SlotLabel: "Slot 01, front", Device: "/dev/sg1",
				Map: jbod.Some("/dev/sda"), Vendor: jbod.Some("ACME"), Model: jbod.Some("Disk"),
				Serial: jbod.Some("S123"), Firmware: jbod.Some("FW1"), Temperature: jbod.Some(int64(37)),
			},
			// A disk whose sensors and mapping are unavailable.
			{Enclosure: "1:0:0:0", Slot: "Slot 02", SlotLabel: "Slot 02, front", Device: "/dev/sg2"},
		},
	}
	var out bytes.Buffer
	if err := cmdList(context.Background(), []string{"-d"}, &out, inv); err != nil {
		t.Fatal(err)
	}
	got := collapse(out.String())
	for _, want := range []string{
		"1:0:0:0 /dev/sg0 ACME Shelf 1 ENC1",
		"2:0:0:0 /dev/sg9 NONE NONE NONE NONE",
		"Disk: /dev/sg1 Map: /dev/sda Slot: Slot 01 Vendor: ACME Model: Disk Serial: S123 Temp: 37 Fw: FW1",
		"Disk: /dev/sg2 Map: NONE Slot: Slot 02 Vendor: N/A Model: N/A Serial: N/A Temp: ERR Fw: N/A",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestListRendersFans(t *testing.T) {
	t.Parallel()
	inv := &fake{
		enclosures: []jbod.Enclosure{{Slot: "1:0:0:0", Device: "/dev/sg0"}},
		fans: []jbod.Fan{
			{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Comment: jbod.Some("low speed"), Speed: 1200},
			// sg_ses printed a speed but no condition after it.
			{Slot: "1:0:0:0", Description: "Fan B", Index: "2,1", Speed: 3000},
		},
	}
	var out bytes.Buffer
	if err := cmdList(context.Background(), []string{"-f"}, &out, inv); err != nil {
		t.Fatal(err)
	}
	got := collapse(out.String())
	for _, want := range []string{
		"SLOT IDENT DESCRIPTION STATUS RPM",
		"1:0:0:0 2,0 Fan A low speed 1200",
		"1:0:0:0 2,1 Fan B 3000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestListReportsCollectionErrors(t *testing.T) {
	t.Parallel()
	want := errors.New("lsscsi is missing")
	inv := &fake{err: want}
	for _, args := range [][]string{{"-e"}, {"-d"}, {"-f"}} {
		if err := cmdList(context.Background(), args, &bytes.Buffer{}, inv); !errors.Is(err, want) {
			t.Errorf("list %v returned %v, want %v", args, err, want)
		}
	}
}

// TestLEDDelegatesToTheClient covers C3: the CLI names the device and the
// kind, and the client resolves the sysfs attribute.
func TestLEDDelegatesToTheClient(t *testing.T) {
	t.Parallel()
	inv := &fake{}
	var out bytes.Buffer
	args := []string{"-l", "/dev/sda", "--locate", "/dev/sdb", "-f", "/dev/sg1", "--on"}
	if err := cmdLED(context.Background(), args, &out, inv); err != nil {
		t.Fatal(err)
	}
	want := []ledCall{
		{device: "/dev/sda", kind: jbod.LEDLocate, on: true},
		{device: "/dev/sdb", kind: jbod.LEDLocate, on: true},
		{device: "/dev/sg1", kind: jbod.LEDFault, on: true},
	}
	if len(inv.leds) != len(want) {
		t.Fatalf("got %+v, want %+v", inv.leds, want)
	}
	for i := range want {
		if inv.leds[i] != want[i] {
			t.Errorf("call %d: got %+v, want %+v", i, inv.leds[i], want[i])
		}
	}
	for _, line := range []string{"/dev/sda locate: true", "/dev/sdb locate: true", "/dev/sg1 fault: true"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("missing %q in\n%s", line, out.String())
		}
	}
	// A failing write stops the run instead of reporting success.
	failing := &fake{err: errors.New("permission denied")}
	if err := cmdLED(context.Background(), []string{"-l", "/dev/sda", "--off"}, &bytes.Buffer{}, failing); err == nil {
		t.Fatal("LED failure reported as success")
	}
}
