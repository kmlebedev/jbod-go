package jbod

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) *Client {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "1:0:0:0", "Slot 01, front")
	if err := os.MkdirAll(filepath.Join(base, "device", "scsi_generic", "sg1"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"locate": "0", "fault": "0", "device/vendor": "ACME\n", "device/model": "Disk\n", "device/vpd_pg80": "\x00\x80\x00\x04S123"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return &Client{Sysfs: root, Run: func(ctx context.Context, name string, args ...string) (string, error) {
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
	}}
}

func TestInventoryAndLED(t *testing.T) {
	c := fixture(t)
	ctx := context.Background()
	es, err := c.Enclosures(ctx)
	if err != nil || len(es) != 1 {
		t.Fatalf("%v %v", es, err)
	}
	if es[0].Model != "Shelf" || es[0].Vendor != "ACME" {
		t.Fatal(es)
	}
	ds, err := c.Disks(ctx, es, true)
	if err != nil || len(ds) != 1 {
		t.Fatalf("%v %v", ds, err)
	}
	d := ds[0]
	if d.Temperature != "37" || d.Serial != "S123" || d.Firmware != "FW1" || d.Map != "/dev/sda" || d.Slot != "Slot 01" {
		t.Fatal(d)
	}
	fs, err := c.Fans(ctx, es)
	if err != nil || len(fs) != 1 || fs[0].Speed != 1200 || fs[0].Comment != "low speed" {
		t.Fatalf("%v %v", fs, err)
	}
	for _, kind := range []string{"locate", "fault"} {
		for _, on := range []bool{true, false} {
			if err := SetLED(ds, "/dev/sda", kind, on); err != nil {
				t.Fatal(err)
			}
			path := d.Locate
			if kind == "fault" {
				path = d.Fault
			}
			b, err := os.ReadFile(path)
			want := "0"
			if on {
				want = "1"
			}
			if err != nil || string(b) != want {
				t.Fatalf("%s %v", b, err)
			}
		}
	}
	if SetLED(ds, "/dev/missing", "locate", true) == nil {
		t.Fatal("missing device accepted")
	}
	if err := os.Remove(d.Locate); err != nil {
		t.Fatal(err)
	}
	if SetLED(ds, "/dev/sg1", "locate", true) == nil {
		t.Fatal("recreated missing LED")
	}
}

func TestMetricsHTTP(t *testing.T) {
	c := fixture(t)
	h := c.Handler()
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
	c.Run = func(context.Context, string, ...string) (string, error) { return "", nil }
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
	if strings.Contains(r.Body.String(), "Slot 01") || !strings.Contains(r.Body.String(), "number_of_enclosures 0") {
		t.Fatal(r.Body.String())
	}
	c.Run = func(context.Context, string, ...string) (string, error) { return "", fmt.Errorf("failed") }
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
	for input, want := range map[string]string{"": "ERR", "short": "ERR", "Current temperature: not available": "ERR", "Current temperature: -2 C": "-2", "Current temperature: 123 C": "123"} {
		if got := temperature(input); got != want {
			t.Fatalf("%q: %s", input, got)
		}
	}
	c := fixture(t)
	old := c.Run
	c.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "scsi_temperature" || name == "sginfo" {
			return "", fmt.Errorf("unavailable")
		}
		return old(ctx, name, args...)
	}
	es, _ := c.Enclosures(context.Background())
	ds, err := c.Disks(context.Background(), es, true)
	if err != nil || ds[0].Temperature != "ERR" || ds[0].Firmware != "N/A" {
		t.Fatalf("%v %v", ds, err)
	}
	path := filepath.Join(t.TempDir(), "vpd")
	for _, data := range []string{"", "bad", "\x00\x80\x00\xffx"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if serial(path) != "N/A" {
			t.Fatal("invalid VPD accepted")
		}
	}
	if got := label("a\\b\"c\nd"); got != `a\\b\"c\nd` {
		t.Fatal(got)
	}
}

func TestLEDSkipTelemetry(t *testing.T) {
	c := fixture(t)
	old := c.Run
	c.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "scsi_temperature" || name == "sginfo" {
			t.Fatalf("LED inventory called %s", name)
		}
		return old(ctx, name, args...)
	}
	es, _ := c.Enclosures(context.Background())
	if _, err := c.Disks(context.Background(), es, false); err != nil {
		t.Fatal(err)
	}
}
