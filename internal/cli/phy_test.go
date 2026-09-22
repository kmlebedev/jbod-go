package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// links is the transport fixture behind the phy report: one host with the
// three shapes the table has to tell apart, and one host with no SAS
// transport at all (ROADMAP 6).
//
//	phy-1:0    up, clean counters
//	phy-1:1    up and counting errors, which is the row an operator looks for
//	phy-1:0:0  an expander phy whose driver publishes no counters: dashes,
//	           never zeros
func links(smp bool) []jbod.SASReport {
	clean := jbod.ErrorCounters{
		Source: "sysfs", InvalidDword: jbod.Some(int64(0)), RunningDisparityError: jbod.Some(int64(0)),
		LossOfDwordSync: jbod.Some(int64(0)), PhyResetProblem: jbod.Some(int64(0)),
	}
	noisy := jbod.ErrorCounters{
		Source: "sysfs", InvalidDword: jbod.Some(int64(1274)), RunningDisparityError: jbod.Some(int64(7)),
		LossOfDwordSync: jbod.Some(int64(31)), PhyResetProblem: jbod.Some(int64(2)),
	}
	expander := jbod.Expander{
		Name: "expander-1:0", Host: jbod.Some(int64(1)),
		SASAddress: jbod.Some("0x5000ccab05629d3f"),
		Vendor:     jbod.Some("HGST"), Product: jbod.Some("H4060-J"), Revision: jbod.Some("4013"),
		Level: jbod.Some(int64(1)), SMPDevice: jbod.Some("/dev/bsg/expander-1:0"),
		NumPhys: jbod.Some(int64(2)),
	}
	host := jbod.SASReport{
		Host: 1, Enclosures: []string{"1:0:0:0"},
		PHYs: []jbod.PHY{
			{
				Name: "phy-1:0", Host: jbod.Some(int64(1)), Port: jbod.Some("port-1:0"),
				SASAddress: jbod.Some("0x500605b00b1e2f40"), DeviceType: jbod.Some("end device"),
				Identifier: jbod.Some(int64(0)), State: jbod.PHYStateUp,
				Negotiated: jbod.LinkRate{Text: jbod.Some("12.0 Gbit"), Gbps: jbod.Some(12.0)},
				Maximum:    jbod.LinkRate{Text: jbod.Some("12.0 Gbit"), Gbps: jbod.Some(12.0)},
				Counters:   clean,
			},
			{
				Name: "phy-1:1", Host: jbod.Some(int64(1)), Port: jbod.Some("port-1:0"),
				SASAddress: jbod.Some("0x500605b00b1e2f41"), DeviceType: jbod.Some("end device"),
				Identifier: jbod.Some(int64(1)), State: jbod.PHYStateUp,
				Negotiated: jbod.LinkRate{Text: jbod.Some("6.0 Gbit"), Gbps: jbod.Some(6.0)},
				Maximum:    jbod.LinkRate{Text: jbod.Some("12.0 Gbit"), Gbps: jbod.Some(12.0)},
				Counters:   noisy,
			},
			{
				Name: "phy-1:0:0", Host: jbod.Some(int64(1)),
				SASAddress: jbod.Some("0x5000ccab05629d3f"), DeviceType: jbod.Some("edge expander"),
				Identifier: jbod.Some(int64(0)), State: jbod.PHYStateDisabled,
				Negotiated: jbod.LinkRate{Text: jbod.Some("Phy disabled")},
				Counters: jbod.ErrorCounters{
					Source: "sysfs", Err: jbod.Some("this phy exposes no link error counters"),
				},
			},
		},
		States: map[jbod.PHYState]int{jbod.PHYStateUp: 2, jbod.PHYStateDisabled: 1},
		Collection: jbod.SASCollection{
			Source: "/sys/class/sas_phy", Complete: true,
		},
	}
	// A shelf behind an HBA that exposes no SAS transport: the report says
	// so instead of showing an empty table that reads as "no links".
	bare := jbod.SASReport{
		Host: 10, Enclosures: []string{"10:0:0:0"},
		States: map[jbod.PHYState]int{},
		Collection: jbod.SASCollection{
			Source: "/sys/class/sas_phy",
			Err:    jbod.Some("/sys/class/sas_phy does not exist: this host exposes no SAS transport"),
		},
	}
	if smp {
		expander.SMP = jbod.SMPStatus{Requested: true, Available: true, OK: true, Phys: 2,
			Command: "smp_rep_general /dev/bsg/expander-1:0"}
		expander.Phys = []jbod.SMPPhy{
			{
				Identifier: 0, Routing: jbod.Some("subtractive"), State: jbod.PHYStateUp,
				Negotiated:      jbod.LinkRate{Text: jbod.Some("12 Gbps"), Gbps: jbod.Some(12.0)},
				AttachedAddress: jbod.Some("0x500605b00b1e2f40"), AttachedPhy: jbod.Some(int64(0)),
				AttachedProtocols: jbod.Some("i(SSP+STP+SMP)"),
				Counters: jbod.ErrorCounters{
					Source: "smp", InvalidDword: jbod.Some(int64(0)), RunningDisparityError: jbod.Some(int64(0)),
					LossOfDwordSync: jbod.Some(int64(0)), PhyResetProblem: jbod.Some(int64(0)),
				},
			},
			{
				// An unattached phy: no address, and the counters are the
				// only thing this row can say.
				Identifier: 1, Routing: jbod.Some("table"), State: jbod.PHYStateUnknown,
				Counters: jbod.ErrorCounters{
					Source: "smp", InvalidDword: jbod.Some(int64(3)), RunningDisparityError: jbod.Some(int64(0)),
					LossOfDwordSync: jbod.Some(int64(1)), PhyResetProblem: jbod.Some(int64(0)),
				},
			},
		}
		host.Collection.SMP = jbod.SMPStatus{Requested: true, Available: true, OK: true, Phys: 2}
		bare.Collection.SMP = jbod.SMPStatus{Requested: true,
			Err: jbod.Some("no SAS expander is registered for this host, so there is nothing to ask over SMP")}
	}
	host.Expanders = []jbod.Expander{expander}
	return []jbod.SASReport{host, bare}
}

// shelfLinks is the inventory of shelf() with the transport fixture behind
// it, so the phy command has both shelves and their hosts.
func shelfLinks(smp bool) *fake {
	f := shelf()
	f.sas = links(smp)
	return f
}

func TestPHYGolden(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		args []string
		file string
		smp  bool
	}{
		{"sysfs", nil, "phy.golden", false},
		{"smp", []string{"--smp"}, "phy-smp.golden", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			inv := shelfLinks(c.smp)
			if err := cmdPHY(context.Background(), c.args, &out, inv); err != nil {
				t.Fatal(err)
			}
			if inv.smp != c.smp {
				t.Errorf("--smp was passed as %v, want %v", inv.smp, c.smp)
			}
			golden(t, c.file, out.String())
		})
	}
}

// TestPHYSelectsByHost covers the selection: naming a shelf reports the
// links of the HBA it is attached through, and says that this is what it
// is doing.
func TestPHYSelectsByHost(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdPHY(context.Background(), []string{"0x5000ccab05629d00"}, &out, shelfLinks(false)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "Host 1") || strings.Contains(got, "Host 10") {
		t.Errorf("selection did not narrow to the shelf's host:\n%s", got)
	}
	if !strings.Contains(got, "which phy carries which shelf is topology") {
		t.Errorf("the report does not state the boundary it cannot cross:\n%s", got)
	}
	// A zero would say the link is clean; the phy without counters gets a
	// dash, three of them.
	if !strings.Contains(collapse(got), "phy-1:0:0 - edge expander 0x5000ccab05629d3f 0 disabled Phy disabled - - - - -") {
		t.Errorf("the counter-less phy is not rendered as absent:\n%s", collapse(got))
	}
}

// TestPHYUnknownEnclosure covers the selector that matches nothing: an
// error, not an empty report that reads as a host without links.
func TestPHYUnknownEnclosure(t *testing.T) {
	t.Parallel()
	if err := cmdPHY(context.Background(), []string{"nosuchshelf"}, &bytes.Buffer{}, shelfLinks(false)); err == nil {
		t.Fatal("an unknown shelf was accepted")
	}
}

// TestPHYJSON covers the machine-readable half: an absent counter is null
// and never zero, which is the whole point of the Optional type here.
func TestPHYJSON(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdPHY(context.Background(), []string{"--json"}, &out, shelfLinks(true)); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Hosts []struct {
			Host int64 `json:"host"`
			PHYs []struct {
				Name       string `json:"name"`
				State      string `json:"state"`
				Negotiated struct {
					Gbps *float64 `json:"gbps"`
				} `json:"negotiated_link_rate"`
				Counters struct {
					InvalidDword *int64  `json:"invalid_dword_count"`
					Err          *string `json:"error"`
				} `json:"error_counters"`
			} `json:"phys"`
			Expanders []struct {
				Phys []struct {
					Attached *string `json:"attached_sas_address"`
				} `json:"smp_phys"`
			} `json:"expanders"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Hosts) != 2 {
		t.Fatalf("hosts %+v", document.Hosts)
	}
	phys := document.Hosts[0].PHYs
	if len(phys) != 3 {
		t.Fatalf("phys %+v", phys)
	}
	if phys[2].Counters.InvalidDword != nil {
		t.Errorf("an absent counter rendered as %v, want null", *phys[2].Counters.InvalidDword)
	}
	if phys[2].Counters.Err == nil {
		t.Error("the absent counter carries no reason")
	}
	if phys[2].Negotiated.Gbps != nil {
		t.Errorf("a disabled phy rendered a rate of %v", *phys[2].Negotiated.Gbps)
	}
	if phys[0].Counters.InvalidDword == nil || *phys[0].Counters.InvalidDword != 0 {
		t.Errorf("a counter that really is zero must stay zero: %+v", phys[0].Counters)
	}
	smp := document.Hosts[0].Expanders[0].Phys
	if len(smp) != 2 || smp[1].Attached != nil {
		t.Errorf("smp phys %+v", smp)
	}
}
