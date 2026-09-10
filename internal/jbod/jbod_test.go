package jbod

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// runners lets a test change command behaviour between requests. The client
// itself is immutable, so a test can no longer reassign its runner field
// mid-flight (C6).
type runners struct {
	mu sync.RWMutex
	fn Runner
}

func newRunners(fn Runner) *runners { return &runners{fn: fn} }

func (r *runners) run(ctx context.Context, name string, args ...string) (string, error) {
	r.mu.RLock()
	fn := r.fn
	r.mu.RUnlock()
	return fn(ctx, name, args...)
}

func (r *runners) set(fn Runner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fn = fn
}

// rig is a client over a temporary sysfs tree with one enclosure and one disk.
type rig struct {
	client  *Client
	runners *runners
	root    string
	// slot and label are how the enclosure and its single slot appear in
	// sysfs; ledPath rebuilds what SetLED writes to.
	slot, label string
}

func (r rig) ledPath(kind LEDKind) string {
	return filepath.Join(r.root, r.slot, r.label, string(kind))
}

func fixture(t *testing.T) rig {
	t.Helper()
	root := t.TempDir()
	slot, label := "1:0:0:0", "Slot 01, front"
	base := filepath.Join(root, slot, label)
	if err := os.MkdirAll(filepath.Join(base, "device", "scsi_generic", "sg1"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"locate": "0", "fault": "0", "device/vendor": "ACME\n", "device/model": "Disk\n", "device/vpd_pg80": "\x00\x80\x00\x04S123"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := newRunners(func(_ context.Context, name string, args ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n[1:0:1:0] disk ACME Disk 1 /dev/sda /dev/sg1\n", nil
		case "sg_inq":
			return "Vendor identification: ACME\nProduct identification: Shelf\nProduct revision level: 1\nUnit serial number: ENC1\n", nil
		case "sg_map":
			return "\n/dev/sg0\n/dev/sg1 /dev/sda\n", nil
		case "scsi_temperature":
			return "header\nCurrent temperature: 37 C\n", nil
		case "sginfo":
			return "Revision level: FW1\n", nil
		case "sg_ses":
			if args[0] == "-j" {
				return "Fan A [2,0] Cooling\nFan A [2,0] Cooling\n", nil
			}
			return "speed code: 2, Actual speed: 1200 rpm, low speed\n", nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	})
	return rig{client: New(WithSysfs(root), WithRunner(r.run)), runners: r, root: root, slot: slot, label: label}
}

func TestInventoryAndLED(t *testing.T) {
	f := fixture(t)
	c := f.client
	ctx := context.Background()
	es, err := c.Enclosures(ctx)
	if err != nil || len(es) != 1 {
		t.Fatalf("%v %v", es, err)
	}
	if es[0].Model.Or("") != "Shelf" || es[0].Vendor.Or("") != "ACME" {
		t.Fatal(es)
	}
	ds, err := c.Disks(ctx, es, DiskOptions{WithTelemetry: true})
	if err != nil || len(ds) != 1 {
		t.Fatalf("%v %v", ds, err)
	}
	d := ds[0]
	if temp, ok := d.Temperature.Get(); !ok || temp != 37 {
		t.Fatalf("temperature %v (present %v)", temp, ok)
	}
	if d.Serial.Or("") != "S123" || d.Firmware.Or("") != "FW1" || d.Map.Or("") != "/dev/sda" || d.Slot != "Slot 01" || d.SlotLabel != f.label {
		t.Fatalf("%+v", d)
	}
	fs, err := c.Fans(ctx, es)
	if err != nil || len(fs) != 1 || fs[0].Speed != 1200 || fs[0].Comment.Or("") != "low speed" {
		t.Fatalf("%v %v", fs, err)
	}
	for _, kind := range []LEDKind{LEDLocate, LEDFault} {
		for _, on := range []bool{true, false} {
			if err := c.SetLED(ctx, "/dev/sda", kind, on); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(f.ledPath(kind))
			want := "0"
			if on {
				want = "1"
			}
			if err != nil || string(b) != want {
				t.Fatalf("%s %v", b, err)
			}
		}
	}
	if c.SetLED(ctx, "/dev/missing", LEDLocate, true) == nil {
		t.Fatal("missing device accepted")
	}
	if c.SetLED(ctx, "sda", LEDLocate, true) == nil {
		t.Fatal("device without a /dev/ prefix accepted")
	}
	if c.SetLED(ctx, "/dev/sg1", LEDKind("blink"), true) == nil {
		t.Fatal("unknown LED kind accepted")
	}
	if err := os.Remove(f.ledPath(LEDLocate)); err != nil {
		t.Fatal(err)
	}
	if c.SetLED(ctx, "/dev/sg1", LEDLocate, true) == nil {
		t.Fatal("recreated missing LED")
	}
}

func TestMetricsHTTP(t *testing.T) {
	f := fixture(t)
	h := f.client.Handler()
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
	for _, want := range []string{"number_of_enclosures 1", `jbod_slot_temperature{slot="Slot 01",enclosure="1:0:0:0"} 37`, `jbod_fan_rpm{device="Fan A",slot="2,0"} 1200`} {
		if !strings.Contains(r.Body.String(), want) {
			t.Fatalf("missing %s in %s", want, r.Body.String())
		}
	}
	if r.Code != 200 || !strings.Contains(r.Header().Get("Content-Type"), "version=0.0.4") {
		t.Fatal(r)
	}
	f.runners.set(func(context.Context, string, ...string) (string, error) { return "", nil })
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(r.Body.String(), "Slot 01") || !strings.Contains(r.Body.String(), "number_of_enclosures 0") {
		t.Fatal(r.Body.String())
	}
	f.runners.set(func(context.Context, string, ...string) (string, error) { return "", fmt.Errorf("failed") })
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
	if r.Code != 503 {
		t.Fatal(r.Code)
	}
	for path, code := range map[string]int{"/": 200, "/missing": 404} {
		r = httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != code {
			t.Fatal(r.Code)
		}
	}
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if r.Code != 405 {
		t.Fatal(r.Code)
	}
}

func TestMalformedAndUnavailable(t *testing.T) {
	for _, tc := range []struct {
		in    string
		want  int64
		found bool
	}{
		{"", 0, false},
		{"short", 0, false},
		{"Current temperature: not available", 0, false},
		{"Current temperature: -2 C", -2, true},
		{"Current temperature: 123 C", 123, true},
	} {
		got, ok := temperature(tc.in)
		if ok != tc.found || got != tc.want {
			t.Errorf("temperature(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.found)
		}
	}
	f := fixture(t)
	old := f.runners.fn
	f.runners.set(func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "scsi_temperature" || name == "sginfo" {
			return "", fmt.Errorf("unavailable")
		}
		return old(ctx, name, args...)
	})
	es, _ := f.client.Enclosures(context.Background())
	ds, err := f.client.Disks(context.Background(), es, DiskOptions{WithTelemetry: true})
	if err != nil {
		t.Fatal(err)
	}
	// A dead sensor is absent, not a string that has to be parsed back.
	if ds[0].Temperature.Present() || ds[0].Firmware.Present() {
		t.Fatalf("unavailable telemetry reported as present: %+v", ds[0])
	}
	// The disk itself is still identified from sysfs.
	if ds[0].Vendor.Or("") != "ACME" || ds[0].Serial.Or("") != "S123" {
		t.Fatalf("%+v", ds[0])
	}
	path := filepath.Join(t.TempDir(), "vpd")
	for _, data := range []string{"", "bad", "\x00\x80\x00\xffx"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := serial(path); ok {
			t.Fatalf("invalid VPD accepted: %q", data)
		}
	}
	if _, ok := serial(filepath.Join(t.TempDir(), "absent")); ok {
		t.Fatal("missing VPD file accepted")
	}
	if got := label("a\\b\"c\nd"); got != `a\\b\"c\nd` {
		t.Fatal(got)
	}
}

func TestLEDSkipTelemetry(t *testing.T) {
	f := fixture(t)
	old := f.runners.fn
	f.runners.set(func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "scsi_temperature" || name == "sginfo" {
			t.Errorf("LED inventory called %s", name)
		}
		return old(ctx, name, args...)
	})
	if err := f.client.SetLED(context.Background(), "/dev/sda", LEDFault, true); err != nil {
		t.Fatal(err)
	}
}
