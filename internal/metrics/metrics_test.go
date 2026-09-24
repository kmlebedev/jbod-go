package metrics

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// snapshot returns a snapshot with one shelf, three disks (one without a
// temperature, two sharing a label) and two fans.
func snapshot() jbod.Snapshot {
	return jbod.Snapshot{
		Enclosures: []jbod.Enclosure{{Slot: "1:0:0:0", Device: "/dev/sg0"}},
		Disks: []jbod.Disk{
			{Enclosure: "1:0:0:0", Slot: "Slot 01", Temperature: jbod.Some(int64(37))},
			{Enclosure: "1:0:0:0", Slot: "Slot 02"},
			{Enclosure: "1:0:0:0", Slot: "Slot 01", Temperature: jbod.Some(int64(41))},
		},
		Fans: []jbod.Fan{
			{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Speed: jbod.Some(int64(1200))},
			{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Speed: jbod.Some(int64(1500))},
		},
		Errors:   map[string]int{jbod.CollectorFans: 1},
		Duration: 1234 * time.Millisecond,
		Up:       true,
	}
}

func TestEncode(t *testing.T) {
	t.Parallel()
	got := encode(t, snapshot(), map[string]int{jbod.CollectorFans: 3}, Options{})
	for _, want := range []string{
		"# TYPE number_of_enclosures gauge\nnumber_of_enclosures 1\n",
		// A disk without a reading is skipped, not exported as zero.
		`jbod_slot_temperature{enclosure="1:0:0:0",slot="Slot 01"} 41`,
		"# TYPE jbod_up gauge\njbod_up 1\n",
		"jbod_scrape_duration_seconds 1.234\n",
		"# TYPE jbod_scrape_errors_total counter\n",
		`jbod_scrape_errors_total{collector="enclosures"} 0`,
		`jbod_scrape_errors_total{collector="disks"} 0`,
		// The counter comes from the running totals, not from this pass.
		`jbod_scrape_errors_total{collector="fans"} 3`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, `slot="Slot 02"`) {
		t.Errorf("absent temperature exported:\n%s", got)
	}
	// Duplicate label sets collapse to one series, last value winning, as
	// the original gauge vector did.
	if n := strings.Count(got, `jbod_slot_temperature{enclosure="1:0:0:0",slot="Slot 01"`); n != 1 {
		t.Errorf("%d series for one label set:\n%s", n, got)
	}
	if !strings.Contains(got, `jbod_fan_speed_rpm{component="Fan A",component_id="2,0",enclosure="1:0:0:0",`) ||
		!strings.Contains(got, `} 1500`) || strings.Count(got, "jbod_fan_speed_rpm{") != 1 {
		t.Errorf("fan series not collapsed:\n%s", got)
	}
	// jbod_fan_rpm is gone, with nothing that brings it back.
	if strings.Contains(got, "jbod_fan_rpm") {
		t.Errorf("the removed series is published:\n%s", got)
	}
}

func TestEncodeIncompleteCollection(t *testing.T) {
	t.Parallel()
	s := snapshot()
	s.Up = false
	got := encode(t, s, s.Errors, Options{})
	if !strings.Contains(got, "jbod_up 0") {
		t.Errorf("an incomplete pass must report jbod_up 0:\n%s", got)
	}
	// Even then the readings that were collected are exported.
	if !strings.Contains(got, "jbod_slot_temperature{") {
		t.Errorf("partial data dropped:\n%s", got)
	}
}

func TestEncodeEscapesLabels(t *testing.T) {
	t.Parallel()
	s := jbod.Snapshot{
		Disks: []jbod.Disk{{Enclosure: "enc\n1", Slot: `Slot "1"\x`, Temperature: jbod.Some(int64(20))}},
		Fans:  []jbod.Fan{{Description: "Fan\\A", Index: `2,"0"`, Speed: jbod.Some(int64(900))}},
		Up:    true,
	}
	got := encode(t, s, nil, Options{})
	for _, want := range []string{
		`jbod_slot_temperature{enclosure="enc\n1",slot="Slot \"1\"\\x"} 20`,
		`jbod_fan_speed_rpm{component="Fan\\A",component_id="2,\"0\"",enclosure="",enclosure_id=""} 900`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

// TestEncodeUnknownCollector keeps a new collector from silently dropping out
// of the output if someone adds one without touching this package.
func TestEncodeUnknownCollector(t *testing.T) {
	t.Parallel()
	got := encode(t, jbod.Snapshot{Up: true}, map[string]int{"psu": 2, jbod.CollectorDisks: 1}, Options{})
	if !strings.Contains(got, `jbod_scrape_errors_total{collector="psu"} 2`) {
		t.Errorf("unknown collector missing:\n%s", got)
	}
	// The known collectors keep their order and come first.
	first := strings.Index(got, `collector="enclosures"`)
	last := strings.Index(got, `collector="psu"`)
	if first < 0 || last < first {
		t.Errorf("collector order:\n%s", got)
	}
}

// TestFanSeriesDoNotCollideAcrossEnclosures is the readiness criterion of
// ROADMAP 4: two shelves with identically named fans are two fans. The
// description and the SES index are per shelf, so "Fan A" at index 2,0 is on
// every shelf in the rack; jbod_fan_rpm, labelled with those two only, let
// the last shelf overwrite the others and is removed.
func TestFanSeriesDoNotCollideAcrossEnclosures(t *testing.T) {
	t.Parallel()
	s := jbod.Snapshot{
		Enclosures: []jbod.Enclosure{
			{Slot: "1:0:0:0", Device: "/dev/sg0", ID: jbod.Some("naa.5000000000000001")},
			{Slot: "10:0:0:0", Device: "/dev/sg9", ID: jbod.Some("naa.5000000000000002")},
		},
		Fans: []jbod.Fan{
			{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Speed: jbod.Some(int64(1200))},
			{Slot: "10:0:0:0", Description: "Fan A", Index: "2,0", Speed: jbod.Some(int64(4800))},
		},
		Up: true,
	}
	got := encode(t, s, nil, Options{})

	for _, want := range []string{
		`jbod_fan_speed_rpm{component="Fan A",component_id="2,0",enclosure="1:0:0:0",enclosure_id="naa.5000000000000001"} 1200`,
		`jbod_fan_speed_rpm{component="Fan A",component_id="2,0",enclosure="10:0:0:0",enclosure_id="naa.5000000000000002"} 4800`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "jbod_fan_speed_rpm{"); n != 2 {
		t.Errorf("got %d fan series, want 2:\n%s", n, got)
	}
	if strings.Contains(got, "jbod_fan_rpm") {
		t.Errorf("the removed series is published:\n%s", got)
	}
}

// TestEnclosureInfoCarriesTheIdentity covers the join target for every
// series labelled with the SCSI address, and the marker that says when that
// identity is really just the address again (ROADMAP 3).
func TestEnclosureInfoCarriesTheIdentity(t *testing.T) {
	t.Parallel()
	s := jbod.Snapshot{
		Enclosures: []jbod.Enclosure{
			{Slot: "1:0:0:0", ID: jbod.Some("naa.5000"), Vendor: jbod.Some("ACME"), Model: jbod.Some("Shelf 24")},
			{Slot: "2:0:0:0", Serial: jbod.Some("ENC2")},
			{Slot: "3:0:0:0"},
		},
		Up: true,
	}
	got := encode(t, s, nil, Options{})
	for _, want := range []string{
		`jbod_enclosure_info{enclosure="1:0:0:0",enclosure_id="naa.5000",id_source="logical",model="Shelf 24",revision="",serial="",vendor="ACME"} 1`,
		`jbod_enclosure_info{enclosure="2:0:0:0",enclosure_id="ENC2",id_source="serial",model="",revision="",serial="ENC2",vendor=""} 1`,
		`jbod_enclosure_info{enclosure="3:0:0:0",enclosure_id="3:0:0:0",id_source="address",model="",revision="",serial="",vendor=""} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

// TestSlotCountsPerOccupancy covers the three states reaching the metrics,
// and the series existing at zero so a state going empty is visible.
func TestSlotCountsPerOccupancy(t *testing.T) {
	t.Parallel()
	s := jbod.Snapshot{
		Enclosures: []jbod.Enclosure{{Slot: "1:0:0:0", ID: jbod.Some("naa.5000")}},
		Slots: []jbod.Slot{
			{Enclosure: "1:0:0:0", Label: "Slot 01", Occupancy: jbod.OccupancyOccupied},
			{Enclosure: "1:0:0:0", Label: "Slot 02", Occupancy: jbod.OccupancyOccupied},
			{Enclosure: "1:0:0:0", Label: "Slot 03", Occupancy: jbod.OccupancyEmpty},
			{Enclosure: "1:0:0:0", Label: "Slot 04", Occupancy: jbod.OccupancyUnavailable},
		},
		Up: true,
	}
	got := encode(t, s, nil, Options{})
	for _, want := range []string{
		`jbod_enclosure_slots{enclosure="1:0:0:0",enclosure_id="naa.5000",occupancy="occupied"} 2`,
		`jbod_enclosure_slots{enclosure="1:0:0:0",enclosure_id="naa.5000",occupancy="empty"} 1`,
		`jbod_enclosure_slots{enclosure="1:0:0:0",enclosure_id="naa.5000",occupancy="unavailable"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	// A shelf with no slots at all still publishes the three series, so a
	// count dropping to zero is a value and not a gap.
	empty := encode(t, jbod.Snapshot{Enclosures: s.Enclosures, Up: true}, nil, Options{})
	if n := strings.Count(empty, "jbod_enclosure_slots{"); n != 3 {
		t.Errorf("got %d occupancy series for an empty shelf, want 3:\n%s", n, empty)
	}
}

// A reading the hardware did not report gets no series. A zero temperature
// is a value an operator acts on, and publishing one for a sensor that said
// nothing is the failure the whole absence model exists to prevent
// (ROADMAP 5).
func TestEncodeSkipsUnreportedReadings(t *testing.T) {
	t.Parallel()
	out := encode(t, fullSnapshot(), nil, Options{})
	if strings.Contains(out, `jbod_sensor_temperature_celsius{component="TEMP B"`) {
		t.Errorf("a sensor without a value was published:\n%s", out)
	}
	// It is still visible as an element, with its condition.
	if !strings.Contains(out, `jbod_component_info{component="TEMP B",component_id="3,1",enclosure="1:0:0:0",enclosure_id="ENC1",health="unknown",status="Unsupported",type="temperature sensor"} 1`) {
		t.Errorf("the unreadable sensor lost its info series:\n%s", out)
	}
}

// Health is a label and never a number: "unknown" has no place on a numeric
// severity scale, and every place it could be put is wrong.
func TestEncodeHealthLevels(t *testing.T) {
	t.Parallel()
	out := encode(t, fullSnapshot(), nil, Options{})
	for _, want := range []string{
		`jbod_enclosure_health{enclosure="1:0:0:0",enclosure_id="ENC1",level="warning",source="hardware"} 1`,
		`jbod_enclosure_health{enclosure="1:0:0:0",enclosure_id="ENC1",level="critical",source="components"} 1`,
		`jbod_enclosure_health{enclosure="10:0:0:0",enclosure_id="10:0:0:0",level="unknown",source="hardware"} 1`,
		// Every level keeps a series, so a shelf that recovers publishes a
		// zero instead of leaving a stale critical series behind.
		`jbod_enclosure_health{enclosure="1:0:0:0",enclosure_id="ENC1",level="critical",source="hardware"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing series:\n%s\nin:\n%s", want, out)
		}
	}
}

// The completeness of the poll is published apart from the condition of the
// hardware, because they are different alerts.
func TestEncodeCollection(t *testing.T) {
	t.Parallel()
	out := encode(t, fullSnapshot(), nil, Options{})
	for _, want := range []string{
		`jbod_collection_complete{enclosure="1:0:0:0",enclosure_id="ENC1"} 1`,
		`jbod_collection_complete{enclosure="10:0:0:0",enclosure_id="10:0:0:0"} 0`,
		`jbod_ses_page_read{enclosure="1:0:0:0",enclosure_id="ENC1",page="threshold in",required="false"} 0`,
		`jbod_enclosure_generation_changed{enclosure="10:0:0:0",enclosure_id="10:0:0:0"} 1`,
		`jbod_enclosure_components_missing{enclosure="1:0:0:0",enclosure_id="ENC1"} 1`,
		"jbod_snapshot_timestamp_seconds ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing series:\n%s\nin:\n%s", want, out)
		}
	}
}

// A bay with no address maps to nothing, and an empty label would join to
// every other empty one.
func TestEncodeMappingNeedsAnAddress(t *testing.T) {
	t.Parallel()
	out := encode(t, fullSnapshot(), nil, Options{})
	if !strings.Contains(out, `jbod_slot_sas_address_info{block_device="/dev/sda",component_id="0,0",device="/dev/sg1",enclosure="1:0:0:0",enclosure_id="ENC1",sas_address="0x5000cca2a0d6e2f5",slot="0"} 1`) {
		t.Errorf("the mapping series is missing:\n%s", out)
	}
	// SLOT 01 declares no address, so it is the one bay without a series.
	if n := strings.Count(out, "jbod_slot_sas_address_info{"); n != 1 {
		t.Errorf("got %d mapping series, want only the bay that has an address:\n%s", n, out)
	}
}

// A snapshot with no inspection — an old client, or a pass that failed
// before the pages were read — renders the pre-1.2 output and nothing else.
func TestEncodeWithoutStatus(t *testing.T) {
	t.Parallel()
	s := fullSnapshot()
	s.Status = nil
	out := encode(t, s, nil, Options{})
	for _, unwanted := range []string{"jbod_enclosure_health", "jbod_component_info", "jbod_collection_complete", "jbod_sensor_"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("%s was published without an inspection:\n%s", unwanted, out)
		}
	}
	if !strings.Contains(out, "jbod_enclosure_info") {
		t.Errorf("the pre-1.2 series are missing:\n%s", out)
	}
}

// TestPHYSeries covers the rule the SAS series exist for: a counter the
// transport did not expose gets no series at all (ROADMAP 6).
//
// The alternative is a zero, and a zero here says the link is clean. On a
// driver that publishes no link error counters that would be a claim made
// by this exporter and by nobody else.
func TestPHYSeries(t *testing.T) {
	t.Parallel()
	out := encode(t, fullSnapshot(), nil, Options{})
	for _, want := range []string{
		`jbod_sas_phy_invalid_dword_total{device_type="end device",host="1",phy="phy-1:1",port="port-1:0",sas_address="0x500605b00b1e2f41"} 1274`,
		`jbod_sas_phy_negotiated_link_rate_gbps{device_type="end device",host="1",phy="phy-1:0",port="port-1:0",sas_address="0x500605b00b1e2f40"} 12`,
		// The state names the diagnosis instead of folding it into
		// "not up".
		`jbod_sas_phy_state{device_type="edge expander",host="1",phy="phy-1:0:0",port="",sas_address="0x5000ccab05629d3f",state="disabled"} 1`,
		`jbod_sas_phy_state{device_type="edge expander",host="1",phy="phy-1:0:1",port="",sas_address="0x5000ccab05629d3f",state="unknown"} 1`,
		// The count per device carries the zeros.
		`jbod_sas_device_phys{device_type="edge expander",host="1",sas_address="0x5000ccab05629d3f",state="up"} 0`,
		`jbod_sas_device_phys{device_type="edge expander",host="1",sas_address="0x5000ccab05629d3f",state="disabled"} 1`,
		// A phy with no link that answered keeps its counters.
		`jbod_sas_phy_invalid_dword_total{device_type="edge expander",host="1",phy="phy-1:0:1",port="",sas_address="0x5000ccab05629d3f"} 0`,
		// The one the expander declined to describe is counted, not listed.
		`jbod_sas_expander_phys_unanswered{host="1",sas_address="0x5000ccab05629d3f"} 1`,
		// The counters are counters: a phy reset or an HBA reload restarts
		// the hardware's total, and that is a reset Prometheus already
		// knows how to read.
		"# TYPE jbod_sas_phy_invalid_dword_total counter",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing:\n%s", want)
		}
	}
	for _, unwanted := range []string{
		// The phy with no counters, and the ones with no rate.
		`jbod_sas_phy_invalid_dword_total{device_type="edge expander",host="1",phy="phy-1:0:0"`,
		`jbod_sas_phy_negotiated_link_rate_gbps{device_type="edge expander"`,
		// The unanswered phy has no series of its own, of any family.
		`phy="phy-1:0:2"`,
		// The series the state replaced, and the zeros a full state set
		// would carry per phy.
		"jbod_sas_phy_up",
		`state="up"} 0` + "\n" + `jbod_sas_phy_state`,
		// Vacant is never published: a vacant phy gets no series.
		`state="vacant"`,
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a value was published for something nobody read:\n%s", unwanted)
		}
	}
	// The state lives in jbod_sas_phy_state only. As a label on the info
	// series it would end that series every time a link changed too.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "jbod_sas_phy_info{") && strings.Contains(line, "state=") {
			t.Errorf("the info series still carries the state: %s", line)
		}
	}
	// The SAS collector has its own error counter, so a transport that
	// could not be read is visible next to the other collectors.
	if !strings.Contains(encode(t, fullSnapshot(), map[string]int{jbod.CollectorSAS: 3}, Options{}),
		`jbod_scrape_errors_total{collector="sas"} 3`) {
		t.Error("the sas collector has no error series")
	}
}

// TestPHYStateAndDeviceCounts checks the two shapes the phy state is
// published in. Every published phy has exactly one state series, and it is
// 1: a full state set would add four zeros per phy, 796 of 995 series on a
// real host. Every device has one count per state, zeros included, and the
// counts of a device add up to its published phys — the unanswered phy is
// in neither, it is jbod_sas_expander_phys_unanswered's.
func TestPHYStateAndDeviceCounts(t *testing.T) {
	t.Parallel()
	out := encode(t, fullSnapshot(), nil, Options{})
	label := func(line, name string) string {
		start := strings.Index(line, name+`="`) + len(name+`="`)
		return line[start : start+strings.Index(line[start:], `"`)]
	}
	perPHY := map[string]int{}
	perDevice := map[string]map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "jbod_sas_phy_state{"):
			perPHY[label(line, "phy")]++
			if !strings.HasSuffix(line, "} 1") {
				t.Errorf("a phy state series is not 1: %s", line)
			}
		case strings.HasPrefix(line, "jbod_sas_device_phys{"):
			address := label(line, "sas_address")
			if perDevice[address] == nil {
				perDevice[address] = map[string]string{}
			}
			perDevice[address][label(line, "state")] = line[strings.LastIndex(line, " ")+1:]
		}
	}
	// Four phys are published; phy-1:0:2 is the unanswered one.
	if len(perPHY) != 4 {
		t.Fatalf("state series for %d phys, want 4: %v", len(perPHY), perPHY)
	}
	for phy, n := range perPHY {
		if n != 1 {
			t.Errorf("%s has %d state series, want one", phy, n)
		}
	}
	want := map[string]map[string]string{
		"0x5000ccab05629d3f": {"up": "0", "disabled": "1", "failed": "0", "spin-up hold": "0", "unknown": "1"},
		"0x500605b00b1e2f40": {"up": "1", "disabled": "0", "failed": "0", "spin-up hold": "0", "unknown": "0"},
		"0x500605b00b1e2f41": {"up": "1", "disabled": "0", "failed": "0", "spin-up hold": "0", "unknown": "0"},
	}
	if len(perDevice) != len(want) {
		t.Errorf("counts for %d devices, want %d: %v", len(perDevice), len(want), perDevice)
	}
	for address, states := range want {
		got := perDevice[address]
		if len(got) != len(states) {
			t.Errorf("%s has %d state counts, want %d: %v", address, len(got), len(states), got)
		}
		for state, n := range states {
			if got[state] != n {
				t.Errorf("%s %s = %q, want %s", address, state, got[state], n)
			}
		}
	}
}

// TestUnansweredCountIsPublishedAtZero keeps the per-expander count a series
// with history: an expander that described every phy reports 0, so the day
// it stops describing some the change is a step, not a series appearing.
func TestUnansweredCountIsPublishedAtZero(t *testing.T) {
	t.Parallel()
	snapshot := fullSnapshot()
	var phys []jbod.PHY
	for _, phy := range snapshot.PHYs {
		if !phy.Unanswered() {
			phys = append(phys, phy)
		}
	}
	snapshot.PHYs = phys
	out := encode(t, snapshot, nil, Options{})
	if !strings.Contains(out, `jbod_sas_expander_phys_unanswered{host="1",sas_address="0x5000ccab05629d3f"} 0`) {
		t.Errorf("an expander with nothing unanswered has no zero series:\n%s", out)
	}
	// Host phys are not an expander and are not counted as one.
	if strings.Contains(out, `jbod_sas_expander_phys_unanswered{host="1",sas_address="0x500605b00b1e2f4`) {
		t.Error("the HBA was counted as an expander")
	}
}

// TestSensorThresholdUnits keeps each limit under the name of its unit. The
// Threshold In page gives voltage and current limits as a percentage of
// nominal; a percentage published as volts, or a limit in a unit no series
// is named for, would be a number with the wrong meaning, so it gets none.
func TestSensorThresholdUnits(t *testing.T) {
	t.Parallel()
	out := encode(t, fullSnapshot(), nil, Options{})
	for _, want := range []string{
		`jbod_sensor_temperature_threshold_celsius{component="TEMP A",component_id="3,0",enclosure="1:0:0:0",enclosure_id="ENC1",threshold="high_critical",type="temperature sensor"} 65`,
		`jbod_sensor_voltage_threshold_percent{component="VOLT 12V",component_id="4,0",enclosure="1:0:0:0",enclosure_id="ENC1",threshold="high_critical",type="voltage sensor"} 5`,
		`jbod_sensor_voltage_threshold_percent{component="VOLT 12V",component_id="4,0",enclosure="1:0:0:0",enclosure_id="ENC1",threshold="low_warning",type="voltage sensor"} 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing:\n%s", want)
		}
	}
	for _, gone := range []string{"jbod_sensor_voltage_threshold_volts", "jbod_sensor_current_threshold_amps"} {
		if strings.Contains(out, gone) {
			t.Errorf("%s is published; the page has no limits in that unit", gone)
		}
	}

	snapshot := fullSnapshot()
	for i := range snapshot.Status[0].Components {
		c := &snapshot.Status[0].Components[i]
		for j := range c.Readings {
			if th := c.Readings[j].Thresholds; th != nil {
				copied := *th
				copied.Unit = jbod.UnitVolts
				c.Readings[j].Thresholds = &copied
			}
		}
	}
	out = encode(t, snapshot, nil, Options{})
	if strings.Contains(out, "_threshold_") {
		t.Errorf("limits in a unit no series is named for were published:\n%s", out)
	}
}

// TestComponentFlags checks the two shapes the element bits are published
// in: per element only the bits that are set, always 1; per enclosure, type
// and bit the count of elements that have it, zeros included. Nothing is
// published for a bay the module cannot reach or for a field that is not a
// bit, and each module of a two-module shelf keeps its own series: both
// share the enclosure identifier and the element index.
func TestComponentFlags(t *testing.T) {
	t.Parallel()
	out := encode(t, fullSnapshot(), nil, Options{})
	for _, want := range []string{
		"# TYPE jbod_component_flag gauge",
		`jbod_component_flag{component="SLOT 00",component_id="0,0",enclosure="1:0:0:0",enclosure_id="ENC1",flag="predicted_failure",type="array device slot"} 1`,
		`jbod_component_flag{component="PSU A",component_id="1,0",enclosure="1:0:0:0",enclosure_id="ENC1",flag="ac_fail",type="power supply"} 1`,
		`jbod_enclosure_component_flags{enclosure="1:0:0:0",enclosure_id="ENC1",flag="predicted_failure",type="array device slot"} 1`,
		`jbod_enclosure_component_flags{enclosure="1:0:0:0",enclosure_id="ENC1",flag="fault_sensed",type="array device slot"} 0`,
		`jbod_enclosure_component_flags{enclosure="1:0:0:0",enclosure_id="ENC1",flag="dc_fail",type="power supply"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing:\n%s", want)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "jbod_component_flag{") && !strings.HasSuffix(line, "} 1") {
			t.Errorf("a clear bit has a series: %s", line)
		}
	}
	for _, unwanted := range []string{
		`component_id="0,1",enclosure="1:0:0:0",enclosure_id="ENC1",flag=`,
		`flag="actual_speed"`, `flag="Actual speed"`, `flag="hot_swap"`,
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("published: %s", unwanted)
		}
	}

	// The same shelf through its second module.
	snapshot := fullSnapshot()
	second := snapshot.Status[0]
	second.Enclosure = "1:0:31:0"
	second.Components = slices.Clone(second.Components)
	for i := range second.Components {
		second.Components[i].Enclosure = "1:0:31:0"
	}
	snapshot.Status = append(snapshot.Status, second)
	out = encode(t, snapshot, nil, Options{})
	for _, enclosure := range []string{"1:0:0:0", "1:0:31:0"} {
		for _, want := range []string{
			`jbod_component_flag{component="PSU A",component_id="1,0",enclosure="` + enclosure +
				`",enclosure_id="ENC1",flag="ac_fail",type="power supply"} 1`,
			`jbod_enclosure_component_flags{enclosure="` + enclosure +
				`",enclosure_id="ENC1",flag="ac_fail",type="power supply"} 1`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the module %s lost its series:\n%s", enclosure, want)
			}
		}
	}
}
