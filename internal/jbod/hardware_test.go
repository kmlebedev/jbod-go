package jbod

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in this file are taken from a WD/HGST H4060-J: a 60-bay
// chassis with two I/O modules, 60 disks installed, running the v1.1
// commands for the first time. Everything here reproduces something that
// shelf actually did.

// chassisID and chassisSerial are what both I/O modules of the shelf report.
const (
	chassisID     = "0x5000ccab05629d00"
	chassisSerial = "THCCT00526EZ0002"
)

// h4060 builds the sysfs tree of that chassis: two enclosures, 60 bays each,
// the first owning bays 0-29 and the second 30-59.
//
// The half a module does not own is the interesting part. Those bays have no
// device link, and their status attribute reads "(null)", because ses.c
// stores the raw SES element status and enclosure.c indexes its name table
// with it: SES status 8, "no access allowed", is past the end of that table.
func h4060(t *testing.T) *Client {
	t.Helper()
	root := t.TempDir()
	type module struct {
		address     string
		device      string
		ownedFrom   int
		ownedTo     int
		sgOfSlot    func(int) string
		blockOfSlot func(int) string
	}
	modules := []module{
		{"1:0:0:0", "/dev/sg2", 0, 29,
			func(n int) string { return fmt.Sprintf("/dev/sg%d", 3+n) },
			func(n int) string { return fmt.Sprintf("/dev/sd%d", 3+n) }},
		{"1:0:31:0", "/dev/sg33", 30, 59,
			func(n int) string { return fmt.Sprintf("/dev/sg%d", 4+n) },
			func(n int) string { return fmt.Sprintf("/dev/sd%d", 4+n) }},
	}
	var mapping strings.Builder
	for _, m := range modules {
		base := filepath.Join(root, m.address)
		mkdir(t, base)
		write(t, filepath.Join(base, "id"), chassisID+"\n", 0o444)
		write(t, filepath.Join(base, "components"), "60\n", 0o444)
		for n := range 60 {
			// The component name carries the drive serial after a comma,
			// on every module, whether or not this one can reach the bay.
			dir := filepath.Join(base, fmt.Sprintf("SLOT %02d,SERIAL%02d", n, n))
			mkdir(t, dir)
			write(t, filepath.Join(dir, "slot"), fmt.Sprintf("%d\n", n), 0o444)
			write(t, filepath.Join(dir, "type"), "array device\n", 0o444)
			write(t, filepath.Join(dir, "locate"), "0\n", 0o644)
			write(t, filepath.Join(dir, "fault"), "0\n", 0o644)
			write(t, filepath.Join(dir, "power_status"), "on\n", 0o644)
			if n < m.ownedFrom || n > m.ownedTo {
				write(t, filepath.Join(dir, "status"), "(null)\n", 0o644)
				continue
			}
			write(t, filepath.Join(dir, "status"), "OK\n", 0o644)
			sg := m.sgOfSlot(n)
			mkdir(t, filepath.Join(dir, "device", "scsi_generic", strings.TrimPrefix(sg, "/dev/")))
			fmt.Fprintf(&mapping, "%s %s\n", sg, m.blockOfSlot(n))
		}
	}
	return New(WithSysfs(root), WithRunner(func(_ context.Context, name string, args ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0]    enclosu HGST     H4060-J          4013  -          /dev/sg2\n" +
				"[1:0:31:0]   enclosu HGST     H4060-J          4013  -          /dev/sg33\n", nil
		case "sg_inq":
			return "  Vendor identification: HGST\n  Product identification: H4060-J\n" +
				"  Product revision level: 4013\n  Unit serial number: " + chassisSerial + "\n", nil
		case "sg_map":
			return mapping.String(), nil
		case "scsi_temperature":
			// What sg_logs --temperature prints, which is what the
			// scsi_temperature wrapper passes through.
			return "    WDC       WUH722626AL5204   CJ20\nTemperature log page  [0xd]\n" +
				"  Current temperature = 33 C\n  Reference temperature = 68 C\n", nil
		case "sginfo":
			return "Revision level: CJ20\n", nil
		case "sg_ses":
			if len(args) > 0 && args[0] == "-j" {
				return coolingElements, nil
			}
			index := strings.TrimPrefix(args[0], "--index=")
			if index == "3,-1" {
				return "speed code: 0, Actual speed: 0 rpm, Fan stopped\n", nil
			}
			return "speed code: 3, Actual speed: 7230 rpm, Fan at third lowest speed\n", nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	}))
}

// coolingElements is the element listing of that shelf: an overall element
// for the type, then the fans themselves.
const coolingElements = ` [3,-1]  Element type: Cooling
  FAN ENCL 1 [3,0]  Element type: Cooling
  FAN ENCL 2 [3,1]  Element type: Cooling
  FAN IOM 1 [3,4]  Element type: Cooling
`

// TestTwoIOModulesDoNotEmptyEachOthersBays is the regression this shelf
// produced: each module lists all sixty bays, and the thirty it cannot
// reach were reported as empty. Thirty populated bays read as thirty empty
// ones, which is exactly the confusion the three occupancy states exist to
// prevent.
func TestTwoIOModulesDoNotEmptyEachOthersBays(t *testing.T) {
	t.Parallel()
	c := h4060(t)
	ctx := context.Background()
	es, err := c.Enclosures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 {
		t.Fatalf("got %d enclosures, want the two I/O modules: %+v", len(es), es)
	}
	slots, err := c.Slots(ctx, es)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 120 {
		t.Fatalf("got %d slots, want 60 per module", len(slots))
	}
	counts := map[string]map[Occupancy]int{}
	for _, s := range slots {
		if counts[s.Enclosure] == nil {
			counts[s.Enclosure] = map[Occupancy]int{}
		}
		counts[s.Enclosure][s.Occupancy]++
	}
	for _, address := range []string{"1:0:0:0", "1:0:31:0"} {
		got := counts[address]
		if got[OccupancyOccupied] != 30 {
			t.Errorf("%s: %d occupied, want 30", address, got[OccupancyOccupied])
		}
		if got[OccupancyUnavailable] != 30 {
			t.Errorf("%s: %d unavailable, want 30", address, got[OccupancyUnavailable])
		}
		if got[OccupancyEmpty] != 0 {
			t.Errorf("%s: %d bays called empty; none of them are", address, got[OccupancyEmpty])
		}
	}
	// An unreachable bay says why, and does not pretend to have a status.
	for _, s := range slots {
		if s.Occupancy != OccupancyUnavailable {
			continue
		}
		if s.Status.Present() {
			t.Errorf("%s slot %s: reported status %s, but the driver could not name it",
				s.Enclosure, s.Label, s.Status)
		}
		reason, ok := s.Err.Get()
		if !ok || !strings.Contains(reason, "no access allowed") {
			t.Errorf("%s slot %s: reason %q", s.Enclosure, s.Label, reason)
		}
		break
	}
	// The disks are the union of what the two modules can reach, and each
	// disk is reported once.
	ds, err := c.Disks(ctx, es, DiskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 60 {
		t.Fatalf("got %d disks, want 60", len(ds))
	}
	devices := map[string]bool{}
	for _, d := range ds {
		if devices[d.Device] {
			t.Errorf("%s reported twice", d.Device)
		}
		devices[d.Device] = true
	}
}

// TestChassisIdentifierAddressesBothModules covers what a shared logical
// identifier means for addressing: it names the chassis, not the sysfs
// enclosure, so it matches both and the LED has to choose deliberately.
func TestChassisIdentifierAddressesBothModules(t *testing.T) {
	t.Parallel()
	c := h4060(t)
	ctx := context.Background()
	es, _ := c.Enclosures(ctx)
	for _, e := range es {
		if id, stable := e.Ref(); id != chassisID || !stable {
			t.Fatalf("%s: Ref() = %q, %v", e.Slot, id, stable)
		}
	}
	selected, err := SelectEnclosures(es, chassisID)
	if err != nil || len(selected) != 2 {
		t.Fatalf("the chassis identifier matched %d enclosures: %v", len(selected), err)
	}
	if siblings := SiblingsOf(es, 0); len(siblings) != 1 || siblings[0].Slot != "1:0:31:0" {
		t.Fatalf("SiblingsOf(0) = %+v", siblings)
	}
	if siblings := SiblingsOf(es, 1); len(siblings) != 1 || siblings[0].Slot != "1:0:0:0" {
		t.Fatalf("SiblingsOf(1) = %+v", siblings)
	}
	if len(SiblingsOf(es, 7)) != 0 {
		t.Error("SiblingsOf accepted an index that is not there")
	}

	// Bay 45 is owned by the second module. Addressing it by the chassis
	// identifier must write through that module: the first one would
	// accept the write and light nothing, because it has no access to the
	// bay at all.
	target, err := ParseLEDTarget("45", chassisID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.SetLED(ctx, target, LEDLocate, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Enclosure != "1:0:31:0" {
		t.Errorf("wrote through %s, want the module that owns bay 45", result.Enclosure)
	}
	if !result.Confirmed {
		t.Errorf("not confirmed: %+v", result)
	}
	// And a bay the other module owns goes the other way.
	target, _ = ParseLEDTarget("3", chassisID)
	result, err = c.SetLED(ctx, target, LEDFault, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Enclosure != "1:0:0:0" {
		t.Errorf("wrote through %s, want the module that owns bay 3", result.Enclosure)
	}
}

// TestTemperatureFromSgLogs covers the format that made every disk of that
// shelf report ERR: sg_logs separates the reading with "=", and the parser
// required ":".
func TestTemperatureFromSgLogs(t *testing.T) {
	t.Parallel()
	c := h4060(t)
	ctx := context.Background()
	es, _ := c.Enclosures(ctx)
	ds, err := c.Disks(ctx, es, DiskOptions{WithTelemetry: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if temp, ok := d.Temperature.Get(); !ok || temp != 33 {
			t.Fatalf("%s: temperature %s, want 33", d.Device, d.Temperature)
		}
		if d.Firmware.Or("") != "CJ20" {
			t.Fatalf("%s: firmware %s", d.Device, d.Firmware)
		}
	}
}

// TestOverallCoolingElementIsNotAFan covers the SES overall element: the
// shelf reports [3,-1] as "Fan stopped" at 0 rpm, which is a summary of the
// type and not a dead fan. Listing it put a stopped fan in front of an
// operator whose fans were all running, and a zero-RPM series in front of
// an alert rule.
func TestOverallCoolingElementIsNotAFan(t *testing.T) {
	t.Parallel()
	refs := parseFanElements(coolingElements)
	if len(refs) != 4 {
		t.Fatalf("got %d elements, want 4: %+v", len(refs), refs)
	}
	if !refs[0].Overall || refs[0].Index != "3,-1" {
		t.Fatalf("the overall element was not recognised: %+v", refs[0])
	}
	for _, ref := range refs[1:] {
		if ref.Overall {
			t.Errorf("%+v was taken for an overall element", ref)
		}
	}
	if kept := individual(refs); len(kept) != 3 {
		t.Fatalf("individual() kept %d elements: %+v", len(kept), kept)
	}

	c := h4060(t)
	ctx := context.Background()
	es, _ := c.Enclosures(ctx)
	fans, err := c.Fans(ctx, es)
	if err != nil {
		t.Fatal(err)
	}
	// Three fans, once: both modules report the same cooling elements of
	// the same chassis, and the second module's copies are deduplicated by
	// the shared serial number.
	if len(fans) != 3 {
		t.Fatalf("got %d fans, want 3: %+v", len(fans), fans)
	}
	for _, f := range fans {
		if f.Index == "3,-1" {
			t.Error("the overall element was listed as a fan")
		}
		if f.Speed.Or(0) != 7230 {
			t.Errorf("%s: speed %s", f.Index, f.Speed)
		}
		if f.Comment.Or("") != "Fan at third lowest speed" {
			t.Errorf("%s: condition %s", f.Index, f.Comment)
		}
	}
}

// TestCapabilitiesOnTheChassis checks the report a two-module chassis
// produces, including the verdicts the kernel's own attribute modes give.
func TestCapabilitiesOnTheChassis(t *testing.T) {
	t.Parallel()
	c := h4060(t)
	ctx := context.Background()
	es, _ := c.Enclosures(ctx)
	reports, err := c.Capabilities(ctx, es)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("got %d reports", len(reports))
	}
	for _, report := range reports {
		if report.Components != 60 {
			t.Errorf("%s: %d components, want 60", report.Enclosure, report.Components)
		}
		if len(report.Shared) != 1 {
			t.Errorf("%s: the other I/O module was not named: %v", report.Enclosure, report.Shared)
		}
		index := map[string]Capability{}
		for _, entry := range report.Capabilities {
			index[entry.Name] = entry
		}
		// slot and type have no store handler, so the kernel creates them
		// read-only; the indicators do and are writable.
		for name, want := range map[string]Support{
			"slot.number": SupportUnsupported,
			"slot.type":   SupportUnsupported,
			"led.locate":  SupportUnknown,
			"led.fault":   SupportUnknown,
		} {
			if got := index[name].Write; got != want {
				t.Errorf("%s: %s write=%s, want %s (%s)", report.Enclosure, name, got, want, index[name].Evidence)
			}
		}
		if index["fan.rpm"].Read != SupportSupported {
			t.Errorf("%s: fan.rpm read=%s", report.Enclosure, index["fan.rpm"].Read)
		}
	}
}

// TestTheDeviceTellsTheModulesApart covers the one identifier that is not
// shared by the two I/O modules. The logical identifier and the serial
// number both name the chassis, so only the generic device selects a single
// sysfs enclosure.
func TestTheDeviceTellsTheModulesApart(t *testing.T) {
	t.Parallel()
	es, err := h4060(t).Enclosures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, shared := range []string{chassisID, chassisSerial} {
		selected, err := SelectEnclosures(es, shared)
		if err != nil || len(selected) != 2 {
			t.Errorf("%s selected %d enclosures (%v), want both modules", shared, len(selected), err)
		}
	}
	for device, want := range map[string]string{"/dev/sg2": "1:0:0:0", "/dev/sg33": "1:0:31:0"} {
		selected, err := SelectEnclosures(es, device)
		if err != nil || len(selected) != 1 {
			t.Fatalf("%s selected %d enclosures (%v)", device, len(selected), err)
		}
		if selected[0].Slot != want {
			t.Errorf("%s selected %s, want %s", device, selected[0].Slot, want)
		}
	}
	// And the SCSI address still works, for a shelf whose path is what the
	// operator has in front of them.
	if selected, err := SelectEnclosures(es, "1:0:31:0"); err != nil || len(selected) != 1 {
		t.Errorf("the SCSI address selected %d enclosures (%v)", len(selected), err)
	}
}
