package jbod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// bays builds a sysfs tree that covers the slot states v1.1 has to tell
// apart, plus the shapes a real enclosure driver produces that the old
// enumeration never saw:
//
//	1  occupied, every attribute present
//	2  empty, the enclosure says "not installed"
//	3  unavailable, the enclosure says so itself
//	4  unavailable, the sysfs entry cannot be walked (a drive pulled mid-scan)
//	5  occupied but with no generic node, so it is a slot and not a disk
//	6  a non-standard component name and no attributes but the device link
//
// The tree is deliberately not uniform: an enclosure that exposes fewer
// attributes than the kernel maximum is the common case, not the exception.
type shelfOption func(*shelfSpec)

type shelfSpec struct {
	id       string
	readOnly map[string]bool
	// sasClass is the /sys/class root the SAS transport is read from. It
	// points at nothing by default, so a fixture shelf is a shelf without
	// SAS transport and the capability report does not depend on whether
	// the machine running the tests happens to have an HBA.
	sasClass string
}

// withSASClass gives the fixture shelf a SAS transport to report on.
func withSASClass(path string) shelfOption {
	return func(s *shelfSpec) { s.sasClass = path }
}

// withoutID builds a shelf that reports no logical identifier, so the
// fallback to the unit serial number and then to the SCSI address is
// exercised.
func withoutID() shelfOption { return func(s *shelfSpec) { s.id = "" } }

// withReadOnlyLED makes the named slot's locate attribute read-only, the
// way the kernel creates an attribute with no store handler.
func withReadOnlyLED(slot string) shelfOption {
	return func(s *shelfSpec) { s.readOnly[slot] = true }
}

func bays(t *testing.T, opts ...shelfOption) *Client {
	t.Helper()
	spec := shelfSpec{id: "naa.50050cc10c400000", readOnly: map[string]bool{}}
	for _, opt := range opts {
		opt(&spec)
	}
	root := t.TempDir()
	base := filepath.Join(root, "1:0:0:0")
	mkdir(t, base)
	if spec.id != "" {
		write(t, filepath.Join(base, "id"), spec.id, 0o444)
	}
	write(t, filepath.Join(base, "components"), "6", 0o444)

	type slotSpec struct {
		name       string
		attributes map[string]string
		// device is "generic" for a full disk, "bare" for a device with no
		// generic node, "broken" for an entry that cannot be walked, and
		// "" for an empty bay.
		device string
		sg     string
	}
	slots := []slotSpec{
		{
			name:       "Slot 01, front",
			attributes: map[string]string{"slot": "1", "type": "array device", "status": "OK", "locate": "0", "fault": "0", "power_status": "on"},
			device:     "generic", sg: "sg1",
		},
		{
			name:       "Slot 02, front",
			attributes: map[string]string{"slot": "2", "type": "array device", "status": "not installed", "locate": "0", "fault": "0", "power_status": "on"},
		},
		{
			name:       "Slot 03, front",
			attributes: map[string]string{"slot": "3", "type": "array device", "status": "unavailable", "locate": "0", "fault": "2"},
		},
		{
			name:       "Slot 04, front",
			attributes: map[string]string{"slot": "4", "type": "array device", "status": "OK"},
			device:     "broken",
		},
		{
			name:       "Slot 05, front",
			attributes: map[string]string{"slot": "5", "type": "array device", "status": "OK", "fault": "3"},
			device:     "bare",
		},
		{
			name:       "bay-06",
			attributes: map[string]string{},
			device:     "generic", sg: "sg6",
		},
	}
	for _, s := range slots {
		dir := filepath.Join(base, s.name)
		mkdir(t, dir)
		for name, value := range s.attributes {
			mode := os.FileMode(0o644)
			if name == "type" || name == "slot" {
				// The kernel has no store handler for these two.
				mode = 0o444
			}
			if name == "locate" && spec.readOnly[s.name] {
				mode = 0o444
			}
			write(t, filepath.Join(dir, name), value+"\n", mode)
		}
		switch s.device {
		case "generic":
			mkdir(t, filepath.Join(dir, "device", "scsi_generic", s.sg))
			write(t, filepath.Join(dir, "device", "vendor"), "ACME\n", 0o444)
			write(t, filepath.Join(dir, "device", "model"), "HDD-16T\n", 0o444)
		case "bare":
			mkdir(t, filepath.Join(dir, "device"))
		case "broken":
			// scsi_generic is a file, so reading it as a directory fails
			// with ENOTDIR: an entry that exists and cannot be walked, the
			// way a slot reads while its device is being torn down. It
			// must not be reported as an empty bay.
			mkdir(t, filepath.Join(dir, "device"))
			write(t, filepath.Join(dir, "device", "scsi_generic"), "", 0o644)
		}
	}
	sasClass := spec.sasClass
	if sasClass == "" {
		sasClass = filepath.Join(root, "no-sas-transport")
	}
	return New(WithSysfs(root), WithSysClass(sasClass), WithRunner(func(_ context.Context, name string, args ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n", nil
		case "sg_inq":
			return "Vendor identification: ACME\nProduct identification: Shelf 24\nProduct revision level: 1.03\nUnit serial number: ENC00001\n", nil
		case "sg_map":
			return "/dev/sg1 /dev/sda\n/dev/sg6 /dev/sdf\n", nil
		case "sg_ses":
			if len(args) > 0 && args[0] == "-j" {
				return "Fan A [2,0] Cooling\n", nil
			}
			return "speed code: 2, Actual speed: 1200 rpm, low speed\n", nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	}))
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, value string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile honours the umask, and the capability probe reads the mode
	// bits, so they are set explicitly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// byLabel indexes slots for the assertions below.
func byLabel(slots []Slot) map[string]Slot {
	index := map[string]Slot{}
	for _, s := range slots {
		index[s.Label] = s
	}
	return index
}

// TestSlotsDistinguishEmptyOccupiedAndUnavailable is the readiness criterion
// of ROADMAP 4: the three states must be three states.
func TestSlotsDistinguishEmptyOccupiedAndUnavailable(t *testing.T) {
	t.Parallel()
	c := bays(t)
	ctx := context.Background()
	es, err := c.Enclosures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slots, err := c.Slots(ctx, es)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 6 {
		t.Fatalf("got %d slots, want 6: %+v", len(slots), slots)
	}
	index := byLabel(slots)
	cases := []struct {
		label     string
		occupancy Occupancy
		device    string
	}{
		{"Slot 01", OccupancyOccupied, "/dev/sg1"},
		{"Slot 02", OccupancyEmpty, ""},
		{"Slot 03", OccupancyUnavailable, ""},
		{"Slot 04", OccupancyUnavailable, ""},
		{"Slot 05", OccupancyOccupied, ""},
		{"bay-06", OccupancyOccupied, "/dev/sg6"},
	}
	for _, tc := range cases {
		s, ok := index[tc.label]
		if !ok {
			t.Errorf("%s was not enumerated", tc.label)
			continue
		}
		if s.Occupancy != tc.occupancy {
			t.Errorf("%s: occupancy %q, want %q", tc.label, s.Occupancy, tc.occupancy)
		}
		if got := s.Device.Or(""); got != tc.device {
			t.Errorf("%s: device %q, want %q", tc.label, got, tc.device)
		}
	}
	// An unavailable slot says why; an empty one has nothing to explain.
	if !index["Slot 04"].Err.Present() {
		t.Error("an unreadable slot reported no reason")
	}
	if index["Slot 02"].Err.Present() {
		t.Error("an empty slot invented a reason")
	}
	// Disks are the occupied slots that expose a generic node, so the
	// slot with no node stays out of the disk view but not out of the
	// inventory.
	ds, err := c.Disks(ctx, es, DiskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 {
		t.Fatalf("got %d disks, want 2: %+v", len(ds), ds)
	}
	if ds[0].Slot != "Slot 01" || ds[0].Map.Or("") != "/dev/sda" {
		t.Errorf("unexpected first disk %+v", ds[0])
	}
	if ds[0].SlotNumber.Or(0) != 1 {
		t.Errorf("disk did not inherit the slot number: %+v", ds[0])
	}
}

// TestSlotAttributesMayBeAbsent covers an enclosure that exposes fewer
// attributes than the kernel maximum: absence must stay absence and never
// become a zero.
func TestSlotAttributesMayBeAbsent(t *testing.T) {
	t.Parallel()
	c := bays(t)
	ctx := context.Background()
	es, _ := c.Enclosures(ctx)
	slots, err := c.Slots(ctx, es)
	if err != nil {
		t.Fatal(err)
	}
	bare := byLabel(slots)["bay-06"]
	for _, field := range []struct {
		name    string
		present bool
	}{
		{"number", bare.Number.Present()},
		{"type", bare.Type.Present()},
		{"status", bare.Status.Present()},
		{"locate", bare.Locate.Present()},
		{"fault", bare.Fault.Value.Present()},
		{"power", bare.Power.Present()},
	} {
		if field.present {
			t.Errorf("%s: reported for a slot that exposes no attributes", field.name)
		}
	}
	// A slot without a number is still addressable by its name, and the
	// name is not required to look like "Slot NN".
	if bare.Name != "bay-06" || bare.Label != "bay-06" {
		t.Errorf("non-standard component name mangled: %+v", bare)
	}
	if got, want := bare.Address(), "naa.50050cc10c400000/bay-06"; got != want {
		t.Errorf("address %q, want %q", got, want)
	}
	full := byLabel(slots)["Slot 01"]
	if got, want := full.Address(), "naa.50050cc10c400000/1"; got != want {
		t.Errorf("address %q, want %q", got, want)
	}
	if full.Power.Or("") != "on" || full.Type.Or("") != "array device" {
		t.Errorf("attributes lost: %+v", full)
	}
}

// TestFaultSeparatesSensedFromRequested covers the SES encoding: the sysfs
// value packs the fault the shelf detected and the fault somebody asked
// for, and collapsing them would turn a marker into an alarm (ROADMAP 4).
func TestFaultSeparatesSensedFromRequested(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw                string
		ok                 bool
		sensed, requested  bool
		present, anyReport bool
	}{
		{"0\n", true, false, false, true, false},
		{"1\n", true, false, true, true, true},
		{"2\n", true, true, false, true, true},
		{"3\n", true, true, true, true, true},
		{"", false, false, false, false, false},
		{"not a number", true, false, false, false, false},
	}
	for _, tc := range cases {
		got := parseFault(tc.raw, tc.ok)
		if got.Value.Present() != tc.present {
			t.Errorf("%q: value present %v, want %v", tc.raw, got.Value.Present(), tc.present)
		}
		if got.Sensed.Or(false) != tc.sensed || got.Requested.Or(false) != tc.requested {
			t.Errorf("%q: sensed %v requested %v, want %v/%v", tc.raw, got.Sensed, got.Requested, tc.sensed, tc.requested)
		}
		if got.Any() != tc.anyReport {
			t.Errorf("%q: Any() = %v, want %v", tc.raw, got.Any(), tc.anyReport)
		}
	}
	// And end to end: slot 3 reports a sensed fault nobody asked for, slot
	// 5 reports both.
	c := bays(t)
	es, _ := c.Enclosures(context.Background())
	slots, err := c.Slots(context.Background(), es)
	if err != nil {
		t.Fatal(err)
	}
	index := byLabel(slots)
	if !index["Slot 03"].Fault.Sensed.Or(false) || index["Slot 03"].Fault.Requested.Or(true) {
		t.Errorf("slot 3: %+v", index["Slot 03"].Fault)
	}
	if !index["Slot 05"].Fault.Sensed.Or(false) || !index["Slot 05"].Fault.Requested.Or(false) {
		t.Errorf("slot 5: %+v", index["Slot 05"].Fault)
	}
}

// TestEnclosureIdentityIsStableOrMarked covers the addressing rule: prefer
// the logical identifier, then the serial, and mark the SCSI address as the
// temporary thing it is (ROADMAP 3).
func TestEnclosureIdentityIsStableOrMarked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	withID, _ := bays(t).Enclosures(ctx)
	if len(withID) != 1 {
		t.Fatalf("%+v", withID)
	}
	if id, stable := withID[0].Ref(); id != "naa.50050cc10c400000" || !stable {
		t.Errorf("Ref() = %q, %v", id, stable)
	}
	if withID[0].IDSource() != IDSourceLogical {
		t.Errorf("id source %q", withID[0].IDSource())
	}

	noID, _ := bays(t, withoutID()).Enclosures(ctx)
	if id, stable := noID[0].Ref(); id != "ENC00001" || !stable {
		t.Errorf("without a logical id, Ref() = %q, %v; want the unit serial", id, stable)
	}
	if noID[0].IDSource() != IDSourceSerial {
		t.Errorf("id source %q", noID[0].IDSource())
	}

	bare := Enclosure{Slot: "1:0:0:0", Device: "/dev/sg0"}
	if id, stable := bare.Ref(); id != "1:0:0:0" || stable {
		t.Errorf("a shelf with no identity must fall back to its address and say so: %q, %v", id, stable)
	}
	if bare.IDSource() != IDSourceAddress {
		t.Errorf("id source %q", bare.IDSource())
	}

	// All three spellings address the same shelf, and the hex identifier
	// is matched without regard to case.
	for _, ref := range []string{"naa.50050cc10c400000", "NAA.50050CC10C400000", "ENC00001", "1:0:0:0"} {
		if !withID[0].Matches(ref) {
			t.Errorf("%q did not match the shelf", ref)
		}
	}
	if withID[0].Matches("") || withID[0].Matches("2:0:0:0") {
		t.Error("matched something it should not")
	}
	if _, err := SelectEnclosures(withID, "nope"); err == nil {
		t.Error("an unknown selector returned an empty listing instead of an error")
	}
	if got, err := SelectEnclosures(withID, ""); err != nil || len(got) != 1 {
		t.Errorf("an empty selector must keep everything: %+v %v", got, err)
	}
}

// TestSlotsSurviveAnUnreadableShelf keeps one bad shelf from hiding the
// others, the way one bad sensor no longer fails a scrape (A6).
func TestSlotsSurviveAnUnreadableShelf(t *testing.T) {
	t.Parallel()
	c := bays(t)
	ctx := context.Background()
	es, _ := c.Enclosures(ctx)
	es = append(es, Enclosure{Slot: "9:0:0:0", Device: "/dev/sg9"})
	slots, err := c.Slots(ctx, es)
	if err == nil {
		t.Error("the unreadable shelf was not reported")
	}
	if len(slots) != 6 {
		t.Fatalf("got %d slots, want the six readable ones: %+v", len(slots), slots)
	}
}
