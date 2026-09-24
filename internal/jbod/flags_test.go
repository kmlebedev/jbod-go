package jbod

import (
	"slices"
	"testing"
)

// joinFlags is one element of each kind a WD H4060-J reports, in the lines
// sg_ses 2.86 (sg3-utils 1.48) prints for them in --join: the format strings
// of enc_status_helper, with the pad the join uses. A few bits are set, so
// a bit that lands on the wrong name shows.
const joinFlags = `SLOT 07,T5G7RMLD [0,7]  Element type: Array device slot
    Predicted failure=1, Disabled=0, Swap=0, status: OK
    OK=0, Reserved device=0, Hot spare=0, Cons check=0
    In crit array=0, In failed array=0, Rebuild/remap=0, R/R abort=0
    App client bypass A=0, Do not remove=0, Enc bypass A=0, Enc bypass B=0
    Ready to insert=0, RMV=0, Ident=1, Report=0
    App client bypass B=0, Fault sensed=1, Fault reqstd=0, Device off=0
    Bypassed A=0, Bypassed B=0, Dev bypassed A=0, Dev bypassed B=0
ENCLOSURE [1,0]  Element type: Enclosure
    Predicted failure=0, Disabled=0, Swap=0, status: OK
    Ident=0, Time until power cycle=1, Failure indication=0
    Warning indication=1, Requested power off duration=0
    Failure requested=0, Warning requested=0
POWER SUPPLY A [2,0]  Element type: Power supply
    Predicted failure=0, Disabled=0, Swap=0, status: Noncritical
    Ident=0, Do not remove=0, DC overvoltage=0, DC undervoltage=0
    DC overcurrent=0, Hot swap=1, Fail=0, Requested on=1, Off=0
    Overtmp fail=0, Temperature warn=1, AC fail=1, DC fail=0
FAN ENCL 1 [3,0]  Element type: Cooling
    Predicted failure=0, Disabled=0, Swap=0, status: Critical
    Ident=0, Do not remove=0, Hot swap=1, Fail=1, Requested on=1
    Off=1, Actual speed=0 rpm, Fan stopped
TEMP SEC1 A DIE [4,67]  Element type: Temperature sensor
    Predicted failure=0, Disabled=0, Swap=0, status: OK
    Ident=0, Fail=0, OT failure=0, OT warning=1, UT failure=0
    UT warning=0
    Temperature=80 C
CONN HOST 00 [7,0]  Element type: SAS connector
    Predicted failure=0, Disabled=0, Swap=0, status: OK
    Ident=0, Mini SAS HD 4x receptacle (SFF-8644) [max 4 phys]
    Connector physical link=0xff, Mated=1, Fail=0, OC=0
VOLT PSU A 12V [8,1]  Element type: Voltage sensor
    Predicted failure=0, Disabled=0, Swap=0, status: OK
    Ident=0, Fail=0,  Warn Over=1, Warn Under=0, Crit Over=0
    Crit Under=0
    Voltage: 12.28 volts
`

// setFlags returns the names of the bits that are set, and all the names.
func setFlags(c Component) (set, all []string) {
	for _, f := range c.StatusFlags() {
		all = append(all, f.Name)
		if f.Set {
			set = append(set, f.Name)
		}
	}
	return set, all
}

func TestStatusFlagsFromJoin(t *testing.T) {
	t.Parallel()
	byIndex := map[string]Component{}
	for _, e := range parseJoinElements(joinFlags) {
		byIndex[e.Index] = Component{Index: e.Index, Type: e.Type, Status: e.Status, Flags: e.Flags}
	}
	for _, tc := range []struct {
		index    string
		set, all []string
	}{
		{"0,7", []string{"fault_sensed", "ident", "predicted_failure"}, []string{
			"app_client_bypassed_a", "app_client_bypassed_b", "bypassed_a", "bypassed_b",
			"cons_check", "device_bypassed_a", "device_bypassed_b", "device_off", "disabled",
			"do_not_remove", "enclosure_bypassed_a", "enclosure_bypassed_b", "fault_requested",
			"fault_sensed", "hot_spare", "ident", "in_crit_array", "in_failed_array", "ok",
			"predicted_failure", "ready_to_insert", "rebuild_remap", "report", "reserved_device",
			"rmv", "rr_abort", "swap",
		}},
		// "Time until power cycle=1" is a number of minutes, not a bit.
		{"1,0", []string{"warning_indication"}, []string{
			"disabled", "failure_indication", "failure_requested", "ident",
			"predicted_failure", "swap", "warning_indication", "warning_requested",
		}},
		{"2,0", []string{"ac_fail", "overtemp_warning", "requested_on"}, []string{
			"ac_fail", "dc_fail", "dc_overcurrent", "dc_overvoltage", "dc_undervoltage",
			"disabled", "do_not_remove", "fail", "ident", "off", "overtemp_failure",
			"overtemp_warning", "predicted_failure", "requested_on", "swap",
		}},
		// "Actual speed=0 rpm" is a speed, and "Hot swap" is what the fan
		// can do, not its state.
		{"3,0", []string{"fail", "off", "requested_on"}, []string{
			"disabled", "do_not_remove", "fail", "ident", "off",
			"predicted_failure", "requested_on", "swap",
		}},
		{"4,67", []string{"overtemp_warning"}, []string{
			"disabled", "fail", "ident", "overtemp_failure", "overtemp_warning",
			"predicted_failure", "swap", "undertemp_failure", "undertemp_warning",
		}},
		{"7,0", []string{"mated"}, []string{
			"disabled", "fail", "ident", "mated", "overcurrent", "predicted_failure", "swap",
		}},
		{"8,1", []string{"warn_over"}, []string{
			"crit_over", "crit_under", "disabled", "fail", "ident", "predicted_failure",
			"swap", "warn_over", "warn_under",
		}},
	} {
		c, ok := byIndex[tc.index]
		if !ok {
			t.Errorf("%s was not parsed", tc.index)
			continue
		}
		set, all := setFlags(c)
		if !slices.Equal(set, tc.set) {
			t.Errorf("%s set %v, want %v", tc.index, set, tc.set)
		}
		if !slices.Equal(all, tc.all) {
			t.Errorf("%s flags %v, want %v", tc.index, all, tc.all)
		}
	}
}

// The spellings that differ between element types and sg_ses versions land
// on one name, and the fields that share the "Name=N" shape without being a
// bit land on none.
func TestFlagName(t *testing.T) {
	t.Parallel()
	for printed, want := range map[string]string{
		"Fault reqstd": "fault_requested", "Fault requested": "fault_requested",
		"App client bypass A": "app_client_bypassed_a", "App client bypassed A": "app_client_bypassed_a",
		"Enc bypass B": "enclosure_bypassed_b", "Enc bypassed B": "enclosure_bypassed_b",
		"Dev bypassed A": "device_bypassed_a", "Device bypassed A": "device_bypassed_a",
		"Overtmp fail": "overtemp_failure", "OT failure": "overtemp_failure",
		"Temperature warn": "overtemp_warning", "OT warning": "overtemp_warning",
		"OC": "overcurrent", "DC overcurrent": "dc_overcurrent",
		"  Warn   Over ": "warn_over", "warn over": "warn_over",
	} {
		if got, ok := FlagName(printed); !ok || got != want {
			t.Errorf("FlagName(%q) = %q, %v; want %q", printed, got, ok, want)
		}
	}
	for _, printed := range []string{
		"Actual speed", "Time until power cycle", "Requested power off duration",
		"Size multiplier", "Invop type", "Display mode status", "pl",
		"Connector physical link", "Temperature", "speed code", "",
	} {
		if got, ok := FlagName(printed); ok {
			t.Errorf("FlagName(%q) = %q; it is not a bit", printed, got)
		}
	}
}

// A module with no access to an element prints bits for it that describe
// nothing; the owning module's answer is the one with meaning.
func TestStatusFlagsNoAccess(t *testing.T) {
	t.Parallel()
	c := Component{Status: Some("No access allowed"), Flags: map[string]bool{"Predicted failure": false, "Ident": false}}
	if flags := c.StatusFlags(); flags != nil {
		t.Errorf("an element without access has flags %v", flags)
	}
	c.Status = Some("OK")
	if flags := c.StatusFlags(); len(flags) != 2 {
		t.Errorf("an accessible element has flags %v, want 2", flags)
	}
}
