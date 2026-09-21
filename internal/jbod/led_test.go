package jbod

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseLEDTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		value     string
		scope     string
		want      LEDTarget
		wantError string
	}{
		{
			name:  "device path, the syntax that predates slots",
			value: "/dev/sda",
			want:  LEDTarget{Raw: "/dev/sda", Device: "/dev/sda"},
		},
		{
			name:  "generic device",
			value: "/dev/sg1",
			want:  LEDTarget{Raw: "/dev/sg1", Device: "/dev/sg1"},
		},
		{
			name:  "self-contained slot",
			value: "naa.5000/5",
			want:  LEDTarget{Raw: "naa.5000/5", Enclosure: "naa.5000", Slot: "5"},
		},
		{
			name:  "slot named after the component",
			value: "1:0:0:0/Slot 01",
			want:  LEDTarget{Raw: "1:0:0:0/Slot 01", Enclosure: "1:0:0:0", Slot: "Slot 01"},
		},
		{
			name:  "bare slot with a scope",
			value: "5",
			scope: "naa.5000",
			want:  LEDTarget{Raw: "naa.5000/5", Enclosure: "naa.5000", Slot: "5"},
		},
		// A device path is never reinterpreted as a slot, whatever the
		// scope is: the old syntax cannot change meaning.
		{
			name:  "a scope does not capture a device",
			value: "/dev/sda",
			scope: "naa.5000",
			want:  LEDTarget{Raw: "/dev/sda", Device: "/dev/sda"},
		},
		{name: "empty", value: "", wantError: "empty"},
		{name: "bare slot without a scope", value: "5", wantError: "--enclosure"},
		{name: "absolute path that is not a device", value: "/sys/class/enclosure", wantError: "/dev/"},
		{name: "device with a subdirectory", value: "/dev/bus/usb", wantError: "invalid device"},
		{name: "empty slot half", value: "naa.5000/", wantError: "<enclosure>/<slot>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLEDTarget(tc.value, tc.scope)
			if tc.wantError != "" {
				if err == nil {
					t.Fatalf("accepted %q, got %+v", tc.value, got)
				}
				if !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error %q, want it to mention %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// led resolves a target and switches it, for the tests below.
func led(t *testing.T, c *Client, target string, kind LEDKind, on bool) (LEDResult, error) {
	t.Helper()
	parsed, err := ParseLEDTarget(target, "")
	if err != nil {
		t.Fatal(err)
	}
	return c.SetLED(context.Background(), parsed, kind, on)
}

// TestLEDConfirmsWithAReadback covers the readiness criterion of ROADMAP 4:
// a system call that returned success is not a confirmed state change.
func TestLEDConfirmsWithAReadback(t *testing.T) {
	t.Parallel()

	t.Run("a shelf that applies the write is confirmed", func(t *testing.T) {
		t.Parallel()
		c := bays(t)
		for _, kind := range []LEDKind{LEDLocate, LEDFault} {
			for _, on := range []bool{true, false} {
				result, err := led(t, c, "1:0:0:0/1", kind, on)
				if err != nil {
					t.Fatalf("%s %v: %v", kind, on, err)
				}
				if !result.Confirmed || result.Observed.Or(!on) != on {
					t.Errorf("%s %v: %+v", kind, on, result)
				}
				if result.Slot != "1" || result.EnclosureID.Or("") != "naa.50050cc10c400000" {
					t.Errorf("result does not name the slot it wrote: %+v", result)
				}
			}
		}
	})

	t.Run("a shelf that ignores the write is not confirmed", func(t *testing.T) {
		t.Parallel()
		// The writer accepts everything and changes nothing, which is what
		// an enclosure that drops the control page looks like from here.
		c := bays(t).With(
			WithLEDWriter(func(string, string) error { return nil }),
			WithLEDReadbackTimeout(20*time.Millisecond),
		)
		result, err := led(t, c, "1:0:0:0/1", LEDLocate, true)
		if !errors.Is(err, ErrLEDNotApplied) {
			t.Fatalf("a dropped write was reported as %v", err)
		}
		if result.Confirmed {
			t.Error("a dropped write was confirmed")
		}
		// The shelf answered, and it answered "off": that is different
		// from not being able to read it back at all.
		if observed, ok := result.Observed.Get(); !ok || observed {
			t.Errorf("observed %v (present %v), want a readback of off", observed, ok)
		}
		if !strings.Contains(err.Error(), "reads back as off") {
			t.Errorf("the error does not say what was observed: %v", err)
		}
	})

	t.Run("an indicator that cannot be read back says so", func(t *testing.T) {
		t.Parallel()
		c := bays(t)
		// A write-only attribute: /dev/null takes the write and returns
		// nothing on read, so there is no observation either way. That is
		// not a failure, but it is not a confirmation either.
		dir := filepath.Join(sysfsOf(t, c), "1:0:0:0", "Slot 01, front")
		if err := os.Remove(filepath.Join(dir, "locate")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(os.DevNull, filepath.Join(dir, "locate")); err != nil {
			t.Fatal(err)
		}
		result, err := led(t, c, "1:0:0:0/1", LEDLocate, true)
		if err != nil {
			t.Fatalf("a write with no readback must not be an error: %v", err)
		}
		if result.Confirmed || result.Observed.Present() {
			t.Errorf("claimed a confirmation without an observation: %+v", result)
		}
	})
}

// TestLEDReachesAnEmptySlot is the point of slot addressing: a bay with no
// disk in it has no device path, so before v1.1 it could not be lit at all.
func TestLEDReachesAnEmptySlot(t *testing.T) {
	t.Parallel()
	c := bays(t)
	result, err := led(t, c, "1:0:0:0/2", LEDLocate, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Confirmed || result.Device.Present() {
		t.Fatalf("an empty slot was lit but reported oddly: %+v", result)
	}
	// The same slot by its component name, and by the stable identifier.
	for _, target := range []string{"1:0:0:0/Slot 02", "naa.50050cc10c400000/2"} {
		if _, err := led(t, c, target, LEDLocate, false); err != nil {
			t.Errorf("%s: %v", target, err)
		}
	}
	// An unknown shelf and an unknown slot are told apart.
	if _, err := led(t, c, "nosuchshelf/2", LEDLocate, true); err == nil || !strings.Contains(err.Error(), "no enclosure matches") {
		t.Errorf("unknown shelf: %v", err)
	}
	if _, err := led(t, c, "1:0:0:0/99", LEDLocate, true); !errors.Is(err, ErrNoSuchTarget) {
		t.Errorf("unknown slot: %v", err)
	}
}

// TestLEDHandlesHotUnplug covers a drive pulled between the listing and the
// write: it must be reported as the slot going away, not as a permission
// problem or, worse, as a success (ROADMAP 4).
func TestLEDHandlesHotUnplug(t *testing.T) {
	t.Parallel()
	c := bays(t).With(WithLEDWriter(func(path, _ string) error {
		// The slot disappears just as the write reaches it.
		if err := os.RemoveAll(filepath.Dir(path)); err != nil {
			return err
		}
		return &os.PathError{Op: "write", Path: path, Err: syscall.ENODEV}
	}))
	_, err := led(t, c, "1:0:0:0/1", LEDLocate, true)
	if !errors.Is(err, ErrSlotGone) {
		t.Fatalf("a pulled drive was reported as %v", err)
	}

	// And a slot that is already gone by the time the write starts.
	c2 := bays(t)
	dir := filepath.Join(sysfsOf(t, c2), "1:0:0:0", "Slot 01, front")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := led(t, c2, "1:0:0:0/1", LEDLocate, true); !errors.Is(err, ErrNoSuchTarget) {
		t.Fatalf("a slot removed before the listing: %v", err)
	}
}

// TestGoneClassifiesErrnos pins which failures mean "the hardware left".
func TestGoneClassifiesErrnos(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want bool
	}{
		{os.ErrNotExist, true},
		{syscall.ENODEV, true},
		{syscall.ENXIO, true},
		{syscall.EACCES, false},
		{syscall.EIO, false},
		{errors.New("something else"), false},
	}
	for _, tc := range cases {
		if got := gone(tc.err); got != tc.want {
			t.Errorf("gone(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// sysfsOf exposes the client's sysfs root to the tests in this package.
func sysfsOf(t *testing.T, c *Client) string {
	t.Helper()
	return c.sysfs
}
