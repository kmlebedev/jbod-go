package metrics

import (
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
	got := encode(t, snapshot(), map[string]int{jbod.CollectorFans: 3}, Options{Deprecated: true})
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
	if !strings.Contains(got, `jbod_fan_rpm{device="Fan A",slot="2,0"} 1500`) || strings.Count(got, "jbod_fan_rpm{") != 1 {
		t.Errorf("fan series not collapsed:\n%s", got)
	}
}

func TestEncodeIncompleteCollection(t *testing.T) {
	t.Parallel()
	s := snapshot()
	s.Up = false
	got := encode(t, s, s.Errors, Options{Deprecated: true})
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
	got := encode(t, s, nil, Options{Deprecated: true})
	for _, want := range []string{
		`jbod_slot_temperature{enclosure="enc\n1",slot="Slot \"1\"\\x"} 20`,
		`jbod_fan_rpm{device="Fan\\A",slot="2,\"0\""} 900`,
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
	got := encode(t, jbod.Snapshot{Up: true}, map[string]int{"psu": 2, jbod.CollectorDisks: 1}, Options{Deprecated: true})
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
// ROADMAP 4: two shelves with identically named fans are two fans.
//
// jbod_fan_rpm labels a cooling element with its description and its SES
// index only, and both are per-shelf, so "Fan A" at index 2,0 collides with
// every other shelf in the rack and the last one written wins. The fix
// cannot be made in place without changing what the existing series means,
// so it is a new name that carries the enclosure.
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
	got := encode(t, s, nil, Options{Deprecated: true})

	for _, want := range []string{
		`jbod_fan_speed_rpm{component="Fan A",component_id="2,0",enclosure="1:0:0:0",enclosure_id="naa.5000000000000001"} 1200`,
		`jbod_fan_speed_rpm{component="Fan A",component_id="2,0",enclosure="10:0:0:0",enclosure_id="naa.5000000000000002"} 4800`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "jbod_fan_speed_rpm{"); n != 2 {
		t.Errorf("got %d corrected fan series, want 2:\n%s", n, got)
	}
	// The old series still collides, which is why it is deprecated rather
	// than quietly relabelled: relabelling it would change the meaning of
	// a series that dashboards already read.
	if n := strings.Count(got, "jbod_fan_rpm{"); n != 1 {
		t.Errorf("got %d deprecated fan series, want the single colliding one:\n%s", n, got)
	}
	if !strings.Contains(got, "# HELP jbod_fan_rpm DEPRECATED") {
		t.Error("the deprecated series is not marked as such")
	}

	// And it can be switched off once nothing reads it.
	without := encode(t, s, nil, Options{})
	if strings.Contains(without, "jbod_fan_rpm{") {
		t.Error("the deprecated series survived Options{Deprecated: false}")
	}
	if n := strings.Count(without, "jbod_fan_speed_rpm{"); n != 2 {
		t.Errorf("the corrected series must stay: %s", without)
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
		// The state set names the diagnosis instead of folding it into
		// "not up".
		`jbod_sas_phy_state{device_type="edge expander",host="1",phy="phy-1:0:0",port="",sas_address="0x5000ccab05629d3f",state="disabled"} 1`,
		`jbod_sas_phy_state{device_type="edge expander",host="1",phy="phy-1:0:0",port="",sas_address="0x5000ccab05629d3f",state="up"} 0`,
		`jbod_sas_phy_state{device_type="edge expander",host="1",phy="phy-1:0:1",port="",sas_address="0x5000ccab05629d3f",state="unknown"} 1`,
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
		// The series the state set replaced.
		"jbod_sas_phy_up",
		// Vacant is never published: a vacant phy gets no series.
		`state="vacant"`,
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a value was published for something nobody read:\n%s", unwanted)
		}
	}
	// The state lives in the state set only. As a label on the info series
	// it would end that series every time a link changed.
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

// TestPHYStateSet checks the invariant the state set exists for: every
// published phy has one series per state, and exactly one of them is 1. A
// phy with two 1s, or none, would make count by (state) disagree with the
// number of phys.
func TestPHYStateSet(t *testing.T) {
	t.Parallel()
	out := encode(t, fullSnapshot(), nil, Options{})
	ones, series := map[string]int{}, map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "jbod_sas_phy_state{") {
			continue
		}
		start := strings.Index(line, `phy="`) + len(`phy="`)
		phy := line[start : start+strings.Index(line[start:], `"`)]
		series[phy]++
		if strings.HasSuffix(line, " 1") {
			ones[phy]++
		}
	}
	// Four phys are published; phy-1:0:2 is the unanswered one.
	if len(series) != 4 {
		t.Fatalf("state set covers %d phys, want 4: %v", len(series), series)
	}
	for phy, n := range series {
		if n != 5 {
			t.Errorf("%s has %d state series, want 5 (up, disabled, failed, spin-up hold, unknown)", phy, n)
		}
		if ones[phy] != 1 {
			t.Errorf("%s has %d states at 1, want exactly one", phy, ones[phy])
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
