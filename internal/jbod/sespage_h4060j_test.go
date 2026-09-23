package jbod

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The fixtures in testdata/h4060j are unedited output of sg_ses 2.86
// (sg3-utils 1.48) on a WD/HGST H4060-J, I/O module A, firmware 4013:
//
//	configuration.txt     sg_ses --page=cf /dev/sg2
//	threshold-in-raw.txt  sg_ses --page=th --raw /dev/sg2
//
// They are the first Threshold In page read from real hardware, and the
// tests below pin what the decoder makes of it (ROADMAP 5, 10).

// h4060j reads one of the H4060-J fixtures.
func h4060j(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "h4060j", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestH4060JConfiguration checks the page the thresholds are decoded
// against: eleven types in this order and with these counts are what
// place each of the 205 descriptors.
func TestH4060JConfiguration(t *testing.T) {
	t.Parallel()
	cfg := parseConfiguration(h4060j(t, "configuration.txt"))
	if got := cfg.Generation.Or(""); got != "0x0" {
		t.Errorf("generation %q, want 0x0", got)
	}
	want := []struct {
		name  string
		count int64
	}{
		{"array device slot", 60}, {"enclosure", 1}, {"power supply", 2}, {"cooling", 8},
		{sesTypeTemperature, 86}, {"enclosure services controller electronics", 2},
		{"sas expander", 6}, {"sas connector", 12}, {sesTypeVoltage, 8},
		{sesTypeCurrent, 8}, {"door", 1},
	}
	if len(cfg.Types) != len(want) {
		t.Fatalf("%d types, want %d: %+v", len(cfg.Types), len(want), cfg.Types)
	}
	for i, w := range want {
		got := cfg.Types[i]
		if got.TypeIndex != int64(i) || got.Type != w.name || got.Possible.Or(-1) != w.count {
			t.Errorf("type %d = %s x %v, want %s x %d", i, got.Type, got.Possible, w.name, w.count)
		}
	}
}

// TestH4060JThresholds pins every limit the shelf declares. The groups
// follow the element descriptors the join reports for the same indices:
// 4,0-59 TEMP SLOT 00-59, 4,60-61 TEMP IOM A/B, 4,62-63 TEMP BB 60,
// 4,64-65 TEMP BB 42, 4,66-77 the expander dies and memories (TEMP SEC1 A
// DIE is 4,67), 4,78-79 TEMP IOM A/B 5V, 4,80-85 TEMP PSU A/B AMB, HOT and
// PRI; 8,0 and 8,2 VOLT PSU A/B AC, the rest the 12 V and 5 V rails; 9,0-3
// the supplies' currents, 9,4-7 the modules'.
func TestH4060JThresholds(t *testing.T) {
	t.Parallel()
	page, err := parseThresholdPage(h4060j(t, "threshold-in-raw.txt"), parseConfiguration(h4060j(t, "configuration.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if page.Generation != "0x0" {
		t.Errorf("generation %q, want 0x0", page.Generation)
	}
	type limits struct{ hc, hw, lw, lc float64 }
	const none = -1
	want := map[string]limits{}
	set := func(ti, from, to int, l limits) {
		for e := from; e <= to; e++ {
			want[fixtureIndex(ti, e)] = l
		}
	}
	set(4, 0, 59, limits{59, 56, 8, 6})
	set(4, 60, 61, limits{105, 95, 5, 1})
	set(4, 62, 63, limits{60, 55, 5, 1})
	set(4, 64, 65, limits{45, 40, 5, 1})
	set(4, 66, 77, limits{105, 95, 5, 1})
	set(4, 78, 79, limits{115, 109, 5, 1})
	for _, psu := range []int{80, 83} {
		set(4, psu, psu, limits{63, 55, 5, 1})
		set(4, psu+1, psu+1, limits{109, 100, 5, 1})
		set(4, psu+2, psu+2, limits{110, 107, 5, 1})
	}
	set(8, 0, 7, limits{10, 5, 7.5, 10})
	set(8, 0, 0, limits{16.5, 13.5, 13.5, 16.5})
	set(8, 2, 2, limits{16.5, 13.5, 13.5, 16.5})
	set(9, 0, 3, limits{20.5, 20, none, none})
	set(9, 4, 7, limits{20, 13, none, none})

	if len(page.Limits) != len(want) {
		t.Errorf("%d elements have limits, want the %d sensors", len(page.Limits), len(want))
	}
	for k, w := range want {
		got, ok := page.Limits[k]
		if !ok {
			t.Errorf("%s has no limits", k)
			continue
		}
		unit := UnitPercentOfNominal
		if k[0] == '4' {
			unit = UnitCelsius
		}
		if got.Unit != unit {
			t.Errorf("%s limits in %q, want %q", k, got.Unit, unit)
		}
		for name, pair := range map[string]struct {
			got  Optional[float64]
			want float64
		}{
			"high critical": {got.HighCritical, w.hc}, "high warning": {got.HighWarning, w.hw},
			"low warning": {got.LowWarning, w.lw}, "low critical": {got.LowCritical, w.lc},
		} {
			v, present := pair.got.Get()
			switch {
			case pair.want == none && present:
				t.Errorf("%s %s = %v, want none", k, name, v)
			case pair.want != none && (!present || v != pair.want):
				t.Errorf("%s %s = %v, want %v", k, name, pair.got, pair.want)
			}
		}
	}
}

// TestH4060JMisreadBySgSes148 keeps the reason the page is decoded here and
// not read from sg_ses's text. sg_ses 1.48 steps through the descriptors
// only for the types that carry thresholds, so on this shelf it reads the
// temperature sensors from the 75 descriptors of the bays, the enclosure,
// the supplies and the fans in front of them. Reproduced on the fixture:
// TEMP SEC1 A DIE (4,67), whose limits are 105/95, would print as reserved,
// and TEMP PSU A AMB (4,80), whose limits are 63/55, would get the bays'
// 59/56. If this test ever fails, the fixture no longer shows the problem
// the raw decoding exists for.
func TestH4060JMisreadBySgSes148(t *testing.T) {
	t.Parallel()
	raw, err := parseHexDump(h4060j(t, "threshold-in-raw.txt"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := parseConfiguration(h4060j(t, "configuration.txt"))
	// Where sg_ses 1.48 reads each temperature sensor: the first descriptor
	// after the overall one, counting only the types it does not skip.
	skewed := func(element int) []byte {
		index := 0
		for _, ty := range cfg.Types {
			switch ty.Type {
			case sesTypeTemperature:
				index += 1 + element
				return raw[4+4*index : 8+4*index]
			case sesTypeVoltage, sesTypeCurrent:
				index += 1 + int(ty.Possible.Or(0))
			}
		}
		t.Fatal("no temperature type")
		return nil
	}
	if d := skewed(67); d[0] != 0 || d[1] != 0 {
		t.Errorf("sg_ses 1.48 would read % x for TEMP SEC1 A DIE, not the bays' zeros", d)
	}
	if d := skewed(80); int(d[0])-20 != 59 || int(d[1])-20 != 56 {
		t.Errorf("sg_ses 1.48 would read % x for TEMP PSU A AMB, not the bays' 59/56", d)
	}
}

// fixtureIndex is the "[type,element]" index the pages share.
func fixtureIndex(typeIndex, element int) string {
	return strconv.Itoa(typeIndex) + "," + strconv.Itoa(element)
}
