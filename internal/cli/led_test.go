package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// TestLEDReportsWhatWasConfirmed is the CLI half of ROADMAP 4: a write the
// kernel accepted must not be printed as a state that was applied.
func TestLEDReportsWhatWasConfirmed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result jbod.LEDResult
		want   string
	}{
		{
			name:   "confirmed",
			result: jbod.LEDResult{Target: "/dev/sda", Kind: jbod.LEDLocate, Requested: true, Observed: jbod.Some(true), Confirmed: true},
			want:   "/dev/sda locate: on (confirmed)",
		},
		{
			name:   "the shelf reports the other state",
			result: jbod.LEDResult{Target: "1:0:0:0/5", Kind: jbod.LEDFault, Requested: true, Observed: jbod.Some(false)},
			want:   "1:0:0:0/5 fault: on (NOT confirmed: reads back as off)",
		},
		{
			name:   "nothing to read back",
			result: jbod.LEDResult{Target: "1:0:0:0/5", Kind: jbod.LEDLocate, Requested: false},
			want:   "1:0:0:0/5 locate: off (write accepted, no readback available)",
		},
		{
			name:   "a slot addressed by number names its disk too",
			result: jbod.LEDResult{Target: "naa.5000/1", Kind: jbod.LEDLocate, Requested: true, Device: jbod.Some("/dev/sg1"), Observed: jbod.Some(true), Confirmed: true},
			want:   "naa.5000/1 locate: on (confirmed) [/dev/sg1]",
		},
		{
			// An identifier shared by two I/O modules resolves to one of
			// them, and the line says which, because the other module
			// would have accepted the write and lit nothing.
			name: "an identifier resolved to one of two paths",
			result: jbod.LEDResult{
				Target: "0x5000ccab05629d00/30", Enclosure: "1:0:31:0", Slot: "30",
				Kind: jbod.LEDLocate, Requested: true, Device: jbod.Some("/dev/sg34"),
				Observed: jbod.Some(true), Confirmed: true,
			},
			want: "0x5000ccab05629d00/30 locate: on (confirmed) [1:0:31:0 slot 30, /dev/sg34]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := ledLine(c.result); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestLEDStopsAtAnUnappliedWrite covers the exit path: an enclosure that
// ignored the control page is a failure, and the lines printed before it
// still stand.
func TestLEDStopsAtAnUnappliedWrite(t *testing.T) {
	t.Parallel()
	inv := inventory()
	inv.ledResult = func(target jbod.LEDTarget, kind jbod.LEDKind, on bool) (jbod.LEDResult, error) {
		result := jbod.LEDResult{Target: target.String(), Kind: kind, Requested: on, Observed: jbod.Some(on), Confirmed: true}
		if target.String() == "/dev/sdc" {
			result.Observed, result.Confirmed = jbod.Some(!on), false
			return result, fmt.Errorf("%w: %s", jbod.ErrLEDNotApplied, target)
		}
		return result, nil
	}
	var out bytes.Buffer
	err := cmdLED(context.Background(), []string{"-l", "/dev/sda", "-l", "/dev/sdc", "-l", "/dev/sg4", "--on"}, &out, inv)
	if !errors.Is(err, jbod.ErrLEDNotApplied) {
		t.Fatalf("an unapplied write returned %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "/dev/sda locate: on (confirmed)") {
		t.Errorf("the write that did work was not reported:\n%s", got)
	}
	if !strings.Contains(got, "/dev/sdc locate: on (NOT confirmed") {
		t.Errorf("the failed write was not reported:\n%s", got)
	}
	// Writes stop at the first failure rather than carrying on.
	if strings.Contains(got, "/dev/sg4") {
		t.Errorf("the run continued past a failure:\n%s", got)
	}
}

// TestLEDSlotAddressingReachesTheClient covers the new addressing reaching
// the inventory unchanged, and the old device syntax still working.
func TestLEDSlotAddressingReachesTheClient(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"device, as before", []string{"-l", "/dev/sda", "--on"}, "/dev/sda"},
		{"self-contained slot", []string{"-l", "1:0:0:0/5", "--on"}, "1:0:0:0/5"},
		{"bare slot with a shelf", []string{"--enclosure", "naa.5000", "-l", "5", "--on"}, "naa.5000/5"},
		{"bare slot with the long selector", []string{"--enclosure-id", "naa.5000", "-f", "5", "--off"}, "naa.5000/5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			inv := inventory()
			if err := cmdLED(context.Background(), c.args, &bytes.Buffer{}, inv); err != nil {
				t.Fatal(err)
			}
			if len(inv.leds) != 1 || inv.leds[0].device != c.want {
				t.Fatalf("got %+v, want a single call for %q", inv.leds, c.want)
			}
		})
	}
}

// TestLEDJSON covers the machine-readable result, including the
// confirmation flag a script has to check.
func TestLEDJSON(t *testing.T) {
	t.Parallel()
	inv := inventory()
	var out bytes.Buffer
	if err := cmdLED(context.Background(), []string{"-l", "1:0:0:0/2", "--on", "--json"}, &out, inv); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Results []struct {
			Target    string `json:"target"`
			LED       string `json:"led"`
			Requested bool   `json:"requested"`
			Observed  *bool  `json:"observed"`
			Confirmed bool   `json:"confirmed"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("%v in:\n%s", err, out.String())
	}
	if len(document.Results) != 1 {
		t.Fatalf("got %d results:\n%s", len(document.Results), out.String())
	}
	r := document.Results[0]
	if r.Target != "1:0:0:0/2" || r.LED != "locate" || !r.Requested || !r.Confirmed || r.Observed == nil || !*r.Observed {
		t.Fatalf("unexpected result %+v", r)
	}
}

// TestLEDRejectsABadReadbackTimeout keeps the flag from silently meaning
// something else.
func TestLEDRejectsABadReadbackTimeout(t *testing.T) {
	t.Parallel()
	err := cmdLED(context.Background(), []string{"-l", "/dev/sda", "--on", "--readback-timeout", "-1s"}, &bytes.Buffer{}, inventory())
	if err == nil || !strings.Contains(err.Error(), "readback timeout") {
		t.Fatalf("got %v", err)
	}
}

// TestEndToEndOverARealClient runs the new commands against *jbod.Client
// over a temporary sysfs tree, so the Inventory interface and the concrete
// implementation are exercised together and not only through the fakes.
func TestEndToEndOverARealClient(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := filepath.Join(root, "1:0:0:0")
	occupied := filepath.Join(base, "Slot 01, front")
	empty := filepath.Join(base, "Slot 02, front")
	for _, dir := range []string{
		filepath.Join(occupied, "device", "scsi_generic", "sg1"),
		empty,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "id"), []byte("naa.5000\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []struct {
		dir            string
		number, status string
	}{{occupied, "1", "OK"}, {empty, "2", "not installed"}} {
		for name, value := range map[string]string{"slot": slot.number, "type": "array device", "status": slot.status, "locate": "0", "fault": "0"} {
			if err := os.WriteFile(filepath.Join(slot.dir, name), []byte(value+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	client := jbod.New(jbod.WithSysfs(root), jbod.WithRunner(func(_ context.Context, name string, args ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n", nil
		case "sg_inq":
			return "Vendor identification: ACME\n", nil
		case "sg_map":
			return "/dev/sg1 /dev/sda\n", nil
		case "sg_ses":
			if len(args) > 0 && args[0] == "-j" {
				return "", nil
			}
			return "", nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	}))

	var out bytes.Buffer
	if err := cmdList(context.Background(), []string{"--slots"}, &out, client); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"naa.5000", "occupied", "empty", "/dev/sda"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}

	out.Reset()
	if err := cmdCapabilities(context.Background(), []string{"--enclosure", "naa.5000"}, &out, client); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"slot.enumeration", "led.locate", "component directories", "readback"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("capabilities over a real client: missing %q in\n%s", want, out.String())
		}
	}

	// The empty bay has no device path, and addressing it by slot number
	// is the only way to light it.
	out.Reset()
	if err := cmdLED(context.Background(), []string{"--enclosure", "naa.5000", "-l", "2", "--on"}, &out, client); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "naa.5000/2 locate: on (confirmed)") {
		t.Errorf("empty-slot LED:\n%s", out.String())
	}
	if b, err := os.ReadFile(filepath.Join(empty, "locate")); err != nil || string(b) != "1" {
		t.Errorf("the attribute holds %q (%v)", b, err)
	}
}
