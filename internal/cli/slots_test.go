package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// inventory extends the rendering fixture with the slot and capability
// views v1.1 adds: an occupied slot with every attribute, an empty bay, a
// bay that could not be read, a slot with a sensed fault, and a shelf that
// reports no stable identifier at all.
func inventory() *fake {
	f := shelf()
	f.enclosures[0].ID = jbod.Some("naa.50050cc10c400000")
	f.slots = []jbod.Slot{
		{
			Enclosure: "1:0:0:0", EnclosureID: jbod.Some("naa.50050cc10c400000"),
			Name: "Slot 01, front", Label: "Slot 01", Number: jbod.Some(int64(1)),
			Type: jbod.Some("array device"), Status: jbod.Some("OK"),
			Occupancy: jbod.OccupancyOccupied, Locate: jbod.Some(false),
			Fault:  jbod.FaultState{Value: jbod.Some(int64(0)), Sensed: jbod.Some(false), Requested: jbod.Some(false)},
			Power:  jbod.Some("on"),
			Device: jbod.Some("/dev/sg1"), Map: jbod.Some("/dev/sda"),
		},
		{
			Enclosure: "1:0:0:0", EnclosureID: jbod.Some("naa.50050cc10c400000"),
			Name: "Slot 02, front", Label: "Slot 02", Number: jbod.Some(int64(2)),
			Type: jbod.Some("array device"), Status: jbod.Some("not installed"),
			Occupancy: jbod.OccupancyEmpty, Locate: jbod.Some(true),
			Fault: jbod.FaultState{Value: jbod.Some(int64(1)), Sensed: jbod.Some(false), Requested: jbod.Some(true)},
			Power: jbod.Some("on"),
		},
		{
			Enclosure: "1:0:0:0", EnclosureID: jbod.Some("naa.50050cc10c400000"),
			Name: "Slot 03, front", Label: "Slot 03", Number: jbod.Some(int64(3)),
			Type: jbod.Some("array device"), Status: jbod.Some("unavailable"),
			Occupancy: jbod.OccupancyUnavailable,
			Fault:     jbod.FaultState{Value: jbod.Some(int64(3)), Sensed: jbod.Some(true), Requested: jbod.Some(true)},
			Err:       jbod.Some("enclosure reports the component as unavailable"),
		},
		{
			// A shelf with no stable identifier and no attributes but the
			// device link: the minimum a driver can expose.
			Enclosure: "10:0:0:0", Name: "bay-06", Label: "bay-06",
			Occupancy: jbod.OccupancyOccupied, Device: jbod.Some("/dev/sg4"),
		},
	}
	f.capabilities = []jbod.EnclosureCapabilities{
		{
			Enclosure: "1:0:0:0", EnclosureID: jbod.Some("naa.50050cc10c400000"),
			Address: "naa.50050cc10c400000", StableID: true, Components: 3,
			Capabilities: []jbod.Capability{
				{Name: "slot.enumeration", Summary: "every slot, empty ones included", Read: jbod.SupportSupported, Write: jbod.SupportUnsupported, Evidence: "sysfs: 3 component directories"},
				{Name: "led.locate", Summary: "identify indicator", Read: jbod.SupportSupported, Write: jbod.SupportUnknown, Evidence: "sysfs: 3/3 components expose locate, 3 readable; the attribute is writable (3/3), but a control page the enclosure ignores still returns success; only a readback after a real write confirms it"},
				{Name: "slot.power_status", Summary: "slot power state", Read: jbod.SupportUnknown, Write: jbod.SupportUnknown, Evidence: "sysfs: 3/3 components expose power_status, 2 readable", Err: jbod.Some("permission denied")},
				{Name: "disk.temperature", Summary: "per-disk temperature", Read: jbod.SupportUnsupported, Write: jbod.SupportUnsupported, Evidence: "scsi_temperature: not installed", Err: jbod.Some("scsi_temperature: not found in /usr/sbin:/usr/bin:/sbin:/bin")},
			},
		},
		{
			Enclosure: "10:0:0:0", Address: "10:0:0:0", StableID: false, Components: 1,
			Capabilities: []jbod.Capability{
				{Name: "slot.enumeration", Summary: "every slot, empty ones included", Read: jbod.SupportSupported, Write: jbod.SupportUnsupported, Evidence: "sysfs: 1 component directories"},
				{Name: "led.locate", Summary: "identify indicator", Read: jbod.SupportUnsupported, Write: jbod.SupportUnsupported, Evidence: "no component exposes locate"},
			},
		},
	}
	return f
}

func TestListSlotsGolden(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdList(context.Background(), []string{"--slots"}, &out, inventory()); err != nil {
		t.Fatal(err)
	}
	golden(t, "list-slots.golden", out.String())
}

func TestCapabilitiesGolden(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdCapabilities(context.Background(), nil, &out, inventory()); err != nil {
		t.Fatal(err)
	}
	golden(t, "capabilities.golden", out.String())
}

// TestSlotTableSeparatesAbsenceFromZero is the rendering half of the
// occupancy rule: an attribute the shelf does not expose is a dash, an
// empty bay is not an unavailable one, and a sensed fault is not a
// requested one.
func TestSlotTableSeparatesAbsenceFromZero(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdList(context.Background(), []string{"--slots"}, &out, inventory()); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"occupied", "empty", "unavailable",
		// slot 2 has a fault LED somebody switched on, slot 3 has a fault
		// the shelf detected and one that was asked for.
		"requested", "sensed+requested",
		// bay-06 exposes nothing but a device link.
		"bay-06",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// The shelf without a stable identifier says so rather than printing
	// its SCSI address as an identity.
	if !strings.Contains(got, "temporary") {
		t.Errorf("a temporary identifier was not marked:\n%s", got)
	}
}

// TestListJSONRendersAbsenceAsNull covers the machine-readable output: a
// reading the hardware did not report must be null and never a zero.
func TestListJSONRendersAbsenceAsNull(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdList(context.Background(), []string{"--slots", "--json"}, &out, inventory()); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Enclosures []json.RawMessage `json:"enclosures"`
		Slots      []struct {
			Label     string  `json:"label"`
			Number    *int64  `json:"number"`
			Type      *string `json:"type"`
			Occupancy string  `json:"occupancy"`
			Device    *string `json:"device"`
			Locate    *bool   `json:"locate"`
			Fault     struct {
				Value     *int64 `json:"value"`
				Sensed    *bool  `json:"sensed"`
				Requested *bool  `json:"requested"`
			} `json:"fault"`
		} `json:"slots"`
		Disks []json.RawMessage `json:"disks"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("%v in:\n%s", err, out.String())
	}
	// Only the requested section is present, so a consumer can tell "not
	// asked for" from "asked for and empty".
	if len(document.Enclosures) != 0 || len(document.Disks) != 0 {
		t.Errorf("unrequested sections were emitted: %s", out.String())
	}
	if len(document.Slots) != 4 {
		t.Fatalf("got %d slots:\n%s", len(document.Slots), out.String())
	}
	occupied, bare := document.Slots[0], document.Slots[3]
	if occupied.Number == nil || *occupied.Number != 1 || occupied.Device == nil {
		t.Errorf("present readings were dropped: %+v", occupied)
	}
	if bare.Number != nil || bare.Type != nil || bare.Locate != nil || bare.Fault.Value != nil {
		t.Errorf("absent readings were not null: %+v", bare)
	}
	if bare.Occupancy != string(jbod.OccupancyOccupied) {
		t.Errorf("occupancy %q", bare.Occupancy)
	}
	// The fault halves survive the round trip separately.
	empty := document.Slots[1]
	if empty.Fault.Sensed == nil || *empty.Fault.Sensed || empty.Fault.Requested == nil || !*empty.Fault.Requested {
		t.Errorf("fault halves collapsed: %+v", empty.Fault)
	}
}

// TestCapabilitiesJSON checks the report survives as data, including the
// probe failures kept apart from the verdicts.
func TestCapabilitiesJSON(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdCapabilities(context.Background(), []string{"--json"}, &out, inventory()); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Enclosures []struct {
			Address      string `json:"address"`
			StableID     bool   `json:"stable_id"`
			Capabilities []struct {
				Name  string  `json:"name"`
				Read  string  `json:"read"`
				Write string  `json:"write"`
				Err   *string `json:"error"`
			} `json:"capabilities"`
		} `json:"enclosures"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("%v in:\n%s", err, out.String())
	}
	if len(document.Enclosures) != 2 {
		t.Fatalf("got %d shelves", len(document.Enclosures))
	}
	if !document.Enclosures[0].StableID || document.Enclosures[1].StableID {
		t.Errorf("stable_id did not survive: %+v", document.Enclosures)
	}
	for _, entry := range document.Enclosures[0].Capabilities {
		if entry.Write == string(jbod.SupportSupported) {
			t.Errorf("%s claims a proven write", entry.Name)
		}
		if entry.Name == "slot.power_status" && entry.Err == nil {
			t.Error("a probe failure was dropped from the JSON")
		}
		if entry.Name == "led.locate" && entry.Err != nil {
			t.Error("a clean probe invented an error")
		}
	}
}

// TestSelectorNarrowsToOneShelf covers --enclosure-id and the error a
// wrong one produces: an unknown shelf must not read as an empty one.
func TestSelectorNarrowsToOneShelf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"by scsi address", []string{"--slots", "--enclosure-id", "1:0:0:0"}},
		{"by logical identifier", []string{"--slots", "--enclosure-id", "naa.50050cc10c400000"}},
		{"by unit serial", []string{"--slots", "--enclosure-id", "ENC00001"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := cmdList(context.Background(), c.args, &out, inventory()); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "10:0:0:0") {
				t.Errorf("the other shelf was not filtered out:\n%s", out.String())
			}
			if !strings.Contains(out.String(), "Slot 01") {
				t.Errorf("the selected shelf is missing:\n%s", out.String())
			}
		})
	}
	var out bytes.Buffer
	err := cmdList(context.Background(), []string{"--slots", "--enclosure-id", "nope"}, &out, inventory())
	if err == nil || !strings.Contains(err.Error(), "no enclosure matches") {
		t.Fatalf("an unknown shelf returned %v and %q", err, out.String())
	}
	// In the commands with no clash, the shorter spelling works too.
	out.Reset()
	if err := cmdCapabilities(context.Background(), []string{"--enclosure", "1:0:0:0"}, &out, inventory()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "10:0:0:0") {
		t.Errorf("--enclosure did not narrow the report:\n%s", out.String())
	}
}

// TestJSONSectionsAreExplicit pins the difference between a section nobody
// asked for and one that was asked for and is empty: the first is missing
// from the document, the second is [].
func TestJSONSectionsAreExplicit(t *testing.T) {
	t.Parallel()
	inv := inventory()
	inv.fans = nil
	var out bytes.Buffer
	if err := cmdList(context.Background(), []string{"-f", "--json"}, &out, inv); err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatalf("%v in:\n%s", err, out.String())
	}
	fans, ok := document["fans"]
	if !ok {
		t.Fatalf("the requested section is missing:\n%s", out.String())
	}
	if string(fans) != "[]" {
		t.Errorf("an empty requested section rendered as %s", fans)
	}
	for _, name := range []string{"enclosures", "slots", "disks"} {
		if _, ok := document[name]; ok {
			t.Errorf("%s was emitted without being requested:\n%s", name, out.String())
		}
	}
}

// chassis renders the shape a WD H4060-J produces: one physical shelf, two
// I/O modules, the same logical identifier on both, and each module
// reporting the thirty bays it cannot reach.
func chassis() *fake {
	const id = "0x5000ccab05629d00"
	noAccess := "the enclosure reports a status code the driver cannot name " +
		"(sysfs status is \"(null)\"); SES code 8 is \"no access allowed\", which is how a bay " +
		"owned by another I/O module of the same shelf reads"
	f := &fake{
		enclosures: []jbod.Enclosure{
			{Slot: "1:0:0:0", Device: "/dev/sg2", ID: jbod.Some(id), Vendor: jbod.Some("HGST"), Model: jbod.Some("H4060-J")},
			{Slot: "1:0:31:0", Device: "/dev/sg33", ID: jbod.Some(id), Vendor: jbod.Some("HGST"), Model: jbod.Some("H4060-J")},
		},
	}
	// Each module lists every bay of the chassis and owns half of them,
	// which is what makes the unavailable half show up twice.
	for _, m := range []struct {
		address          string
		ownsFrom, ownsTo int
	}{{"1:0:0:0", 0, 1}, {"1:0:31:0", 2, 3}} {
		for n := range 4 {
			s := jbod.Slot{
				Enclosure: m.address, EnclosureID: jbod.Some(id),
				Name:   fmt.Sprintf("SLOT %02d,SERIAL%02d", n, n),
				Label:  fmt.Sprintf("SLOT %02d", n),
				Number: jbod.Some(int64(n)), Type: jbod.Some("array device"),
				Locate: jbod.Some(false),
				Fault:  jbod.FaultState{Value: jbod.Some(int64(0)), Sensed: jbod.Some(false), Requested: jbod.Some(false)},
				Power:  jbod.Some("on"),
			}
			if n >= m.ownsFrom && n <= m.ownsTo {
				s.Status = jbod.Some("OK")
				s.Occupancy = jbod.OccupancyOccupied
				s.Device = jbod.Some(fmt.Sprintf("/dev/sg%d", 3+n))
				s.Map = jbod.Some(fmt.Sprintf("/dev/sd%c", 'c'+n))
			} else {
				s.Occupancy = jbod.OccupancyUnavailable
				s.Err = jbod.Some(noAccess)
			}
			f.slots = append(f.slots, s)
		}
	}
	return f
}

// TestChassisWithTwoModulesGolden pins how one shelf reached through two
// I/O modules is rendered: the heading names the other path, the bays a
// module cannot reach are unavailable rather than empty, and the reason is
// stated once instead of thirty times.
func TestChassisWithTwoModulesGolden(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdList(context.Background(), []string{"--slots"}, &out, chassis()); err != nil {
		t.Fatal(err)
	}
	golden(t, "list-slots-chassis.golden", out.String())
	got := out.String()
	if strings.Count(got, "same chassis as") != 2 {
		t.Errorf("the shared identifier was not reported on both paths:\n%s", got)
	}
	if strings.Contains(got, "empty") {
		t.Errorf("a bay behind the other module was called empty:\n%s", got)
	}
	// The reason is a footnote, not a column repeated on every row.
	if n := strings.Count(got, "no access allowed"); n != 2 {
		t.Errorf("the reason appears %d times, want once per enclosure:\n%s", n, got)
	}
}

// TestPluralFlagSpellings covers the spellings an operator reaches for next
// to --disks and --slots; answering "unknown flag" to --fans was a small
// cruelty the real run ran into twice.
func TestPluralFlagSpellings(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--fans"}, {"--fan"}, {"--enclosures"}, {"--slot"}, {"--disk"}} {
		if err := cmdList(context.Background(), args, &bytes.Buffer{}, inventory()); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
}
