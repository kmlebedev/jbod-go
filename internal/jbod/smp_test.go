package jbod

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// The SMP fixtures. They are the shapes smp_utils prints, and they are the
// only description of them this package has: there is no expander in CI.

const smpGeneralOut = `Report general response:
  expander change count: 3
  expander route indexes: 0
  long response: 0
  number of phys: 4
  table to table supported: 1
  zone configuring: 0
  self configuring: 0
  open reject retry supported: 1
  enclosure logical identifier (hex): 5000ccab05629d3f
`

// The zone group on the first line is not decoration: a real WD expander
// appends it after the rate, and reading the whole tail as the rate made it
// "12 Gbps  ZG:14".
const smpDiscoverOut = `  phy   0:S:attached:[500605b00b1e2f40:00  i(SSP+STP+SMP)]  12 Gbps  ZG:14
  phy   1:T:attached:[5000cca25de1c2ed:00  t(SSP)]  6 Gbps
  phy   2:D:attached:[0000000000000000:00]
  phy   3:U:attached:[5000cca25de1c2f1:00  t(SATA)]  phy enabled; unknown rate
`

// errorLog is the answer for one phy, with the counters scaled by the phy
// number so a mixed-up response is visible in the assertions.
func errorLog(phy int, spelling string) string {
	sync := "loss of dword synchronization count"
	if spelling == "short" {
		sync = "loss of dword sync count"
	}
	return fmt.Sprintf(`Report phy error log response:
  expander change count: 3
  phy identifier: %d
  invalid dword count: %d
  running disparity error count: %d
  %s: %d
  phy reset problem count: 0
`, phy, phy*10, phy*2, sync, phy)
}

// smpRunner answers the three smp_utils calls from the fixtures above and
// records every command line, so a test can assert on what was and was not
// asked for.
type smpRunner struct {
	mu       sync.Mutex
	commands []string
	// discover replaces the smp_discover answer; an error fails that call.
	discover    func() (string, error)
	general     func() (string, error)
	errorLogFor func(phy int) (string, error)
}

func (r *smpRunner) run(_ context.Context, name string, args ...string) (string, error) {
	r.mu.Lock()
	r.commands = append(r.commands, name+" "+strings.Join(args, " "))
	r.mu.Unlock()
	switch name {
	case smpReportGeneral:
		if r.general != nil {
			return r.general()
		}
		return smpGeneralOut, nil
	case smpDiscover:
		if r.discover != nil {
			return r.discover()
		}
		return smpDiscoverOut, nil
	case smpPhyErrorLog:
		phy := 0
		for _, arg := range args {
			if v, ok := strings.CutPrefix(arg, "--phy="); ok {
				fmt.Sscanf(v, "%d", &phy)
			}
		}
		if r.errorLogFor != nil {
			return r.errorLogFor(phy)
		}
		return errorLog(phy, "long"), nil
	}
	return "", fmt.Errorf("unexpected command %s", name)
}

// ran reports whether any recorded command contains the given text.
func (r *smpRunner) ran(text string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, command := range r.commands {
		if strings.Contains(command, text) {
			return true
		}
	}
	return false
}

// smpReport runs the phy report of host 1 with SMP enabled.
func smpReport(t *testing.T, runner *smpRunner) SASReport {
	t.Helper()
	c := links(t).With(WithRunner(runner.run))
	reports, err := c.SAS(context.Background(), []Enclosure{shelfOn("1:0:0:0")}, SASOptions{WithSMP: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("got %d reports", len(reports))
	}
	return reports[0]
}

// TestSMPErrorCounters covers the second half of ROADMAP 6: the counters
// the expander keeps, read over SMP and never cleared.
func TestSMPErrorCounters(t *testing.T) {
	t.Parallel()
	runner := &smpRunner{}
	report := smpReport(t, runner)
	if len(report.Expanders) != 1 {
		t.Fatalf("expanders %+v", report.Expanders)
	}
	expander := report.Expanders[0]
	if !expander.SMP.OK || !expander.SMP.Available {
		t.Fatalf("smp status %+v", expander.SMP)
	}
	if n, _ := expander.NumPhys.Get(); n != 4 {
		t.Errorf("num phys %v, want 4", expander.NumPhys)
	}
	if len(expander.Phys) != 4 {
		t.Fatalf("got %d phys: %+v", len(expander.Phys), expander.Phys)
	}
	first := expander.Phys[0]
	// The routing letter is passed through as smp_discover prints it; see
	// parseSMPDiscoverList.
	if routing, _ := first.Routing.Get(); routing != "S" {
		t.Errorf("phy 0 routing %v", first.Routing)
	}
	if text, _ := first.Negotiated.Text.Get(); text != "12 Gbps" {
		t.Errorf("phy 0 rate %q, want the rate without the zone group", text)
	}
	if address, _ := first.AttachedAddress.Get(); address != "0x500605b00b1e2f40" {
		t.Errorf("phy 0 attached %v", first.AttachedAddress)
	}
	if protocols, _ := first.AttachedProtocols.Get(); protocols != "i(SSP+STP+SMP)" {
		t.Errorf("phy 0 protocols %v", first.AttachedProtocols)
	}
	if first.State != PHYStateUp {
		t.Errorf("phy 0 state %s", first.State)
	}
	second := expander.Phys[1]
	if gbps, _ := second.Negotiated.Gbps.Get(); gbps != 6 {
		t.Errorf("phy 1 rate %v", second.Negotiated)
	}
	if n, _ := second.Counters.InvalidDword.Get(); n != 10 {
		t.Errorf("phy 1 invalid dwords %v, want its own 10", second.Counters.InvalidDword)
	}
	if n, _ := second.Counters.LossOfDwordSync.Get(); n != 1 {
		t.Errorf("phy 1 sync losses %v", second.Counters.LossOfDwordSync)
	}
	// An unattached phy reports the null address, which is not an identity
	// and must not be published as one.
	if expander.Phys[2].AttachedAddress.Present() || expander.Phys[2].AttachedPhy.Present() {
		t.Errorf("the null address was published: %+v", expander.Phys[2])
	}
	// "phy enabled; unknown rate" is not a rate, and not a link that is up.
	if expander.Phys[3].State != PHYStateUnknown {
		t.Errorf("phy 3 state %s, want unknown", expander.Phys[3].State)
	}
	if expander.SMP.Phys != 4 || report.Collection.SMP.Phys != 4 {
		t.Errorf("read %d/%d phy error logs, want 4", expander.SMP.Phys, report.Collection.SMP.Phys)
	}
	if !report.Collection.SMP.Requested || !report.Collection.SMP.Available || !report.Collection.SMP.OK {
		t.Errorf("collection smp %+v", report.Collection.SMP)
	}
	// The one option this tool must never pass: --zero clears the counters
	// it reports, and a diagnostic that destroys history is worse than
	// none (ROADMAP 6).
	if runner.ran("--zero") || runner.ran("-z") {
		t.Fatalf("a counter-clearing option was used: %v", runner.commands)
	}
	if !runner.ran(smpPhyErrorLog + " --phy=3 /dev/bsg/expander-1:0") {
		t.Errorf("the per-phy error log was not addressed to the bsg node: %v", runner.commands)
	}
}

// TestSMPShortSpelling covers the other spelling of the synchronisation
// counter, because a spelling this parser does not know becomes a link with
// no losses rather than a link nobody asked.
func TestSMPShortSpelling(t *testing.T) {
	t.Parallel()
	report := smpReport(t, &smpRunner{errorLogFor: func(phy int) (string, error) {
		return errorLog(phy, "short"), nil
	}})
	if n, _ := report.Expanders[0].Phys[2].Counters.LossOfDwordSync.Get(); n != 2 {
		t.Errorf("phy 2 sync losses %v, want 2", report.Expanders[0].Phys[2].Counters.LossOfDwordSync)
	}
}

// TestSMPDiscoverUnparsed covers a change in the one-line format: the
// attached addresses are lost, and the error counters — the other half of
// this feature — are not.
func TestSMPDiscoverUnparsed(t *testing.T) {
	t.Parallel()
	report := smpReport(t, &smpRunner{discover: func() (string, error) {
		return "a shape this parser has never seen\n", nil
	}})
	expander := report.Expanders[0]
	if len(expander.Phys) != 4 {
		t.Fatalf("got %d phys, want the 4 the expander reported: %+v", len(expander.Phys), expander.Phys)
	}
	if expander.Phys[1].AttachedAddress.Present() {
		t.Errorf("an address was invented: %+v", expander.Phys[1])
	}
	if n, _ := expander.Phys[1].Counters.InvalidDword.Get(); n != 10 {
		t.Errorf("counters lost with the discover output: %+v", expander.Phys[1].Counters)
	}
	if !strings.Contains(expander.Phys[0].Source, smpDiscover) {
		t.Errorf("the phy does not say where it came from: %q", expander.Phys[0].Source)
	}
}

// TestSMPWrongPhy covers a response about a phy nobody asked for. The
// counters are discarded rather than attributed to the wrong link.
func TestSMPWrongPhy(t *testing.T) {
	t.Parallel()
	report := smpReport(t, &smpRunner{errorLogFor: func(int) (string, error) {
		return errorLog(2, "long"), nil
	}})
	phys := report.Expanders[0].Phys
	if phys[0].Counters.Present() {
		t.Errorf("phy 0 took phy 2's counters: %+v", phys[0].Counters)
	}
	if !phys[0].Counters.Err.Present() {
		t.Error("no reason is given for the discarded counters")
	}
	if n, _ := phys[2].Counters.InvalidDword.Get(); n != 20 {
		t.Errorf("phy 2 should keep its own answer, got %v", phys[2].Counters.InvalidDword)
	}
	if report.Collection.SMP.Phys != 1 {
		t.Errorf("counted %d phys read, want the one that matched", report.Collection.SMP.Phys)
	}
}

// TestSMPUnavailable covers a machine without smp_utils: the sysfs half of
// the report survives, and the SMP half says why it is missing.
func TestSMPUnavailable(t *testing.T) {
	t.Parallel()
	report := smpReport(t, &smpRunner{general: func() (string, error) {
		return "", fmt.Errorf("smp_rep_general: not found in /usr/sbin:/usr/bin:/sbin:/bin")
	}})
	if len(report.PHYs) != 6 {
		t.Errorf("the sysfs phys were lost with SMP: %d", len(report.PHYs))
	}
	if report.Collection.SMP.Available || report.Collection.SMP.OK {
		t.Errorf("smp %+v", report.Collection.SMP)
	}
	reason, ok := report.Collection.SMP.Err.Get()
	if !ok || !strings.Contains(reason, "smp_rep_general") {
		t.Errorf("the reason does not name the tool: %q", reason)
	}
	if report.Collection.Complete {
		t.Error("SMP was asked for and did not answer, so the report is not complete")
	}
}

// TestSMPWithoutTarget covers an expander the kernel exposes no bsg node
// for: there is nothing to address, which is not a failure of the expander.
func TestSMPWithoutTarget(t *testing.T) {
	t.Parallel()
	runner := &smpRunner{}
	c := links(t).With(WithRunner(runner.run), WithSysClass(withoutBSG(t)))
	reports, err := c.SAS(context.Background(), nil, SASOptions{WithSMP: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range reports {
		if report.Host != 1 {
			continue
		}
		if len(runner.commands) != 0 {
			t.Fatalf("SMP was attempted without a target: %v", runner.commands)
		}
		reason, ok := report.Collection.SMP.Err.Get()
		if !ok || !strings.Contains(reason, "bsg") {
			t.Errorf("no usable reason for the missing SMP target: %q", reason)
		}
	}
}

// withoutBSG is the same class tree as links, minus the bsg entry.
func withoutBSG(t *testing.T) string {
	t.Helper()
	class := t.TempDir()
	mkdir(t, class)
	source := links(t)
	for _, name := range []string{"sas_phy", "sas_expander", "sas_device"} {
		symlink(t, source.classDir(name), class+"/"+name)
	}
	return class
}

// TestSMPParsers pins the shapes of the smp_utils output this package
// depends on, without a client in the way.
func TestSMPParsers(t *testing.T) {
	t.Parallel()
	if n, _ := parseSMPGeneral(smpGeneralOut).Get(); n != 4 {
		t.Errorf("number of phys %v", parseSMPGeneral(smpGeneralOut))
	}
	if parseSMPGeneral("nothing here").Present() {
		t.Error("a phy count was invented")
	}
	phys := parseSMPDiscoverList(smpDiscoverOut, "source")
	if len(phys) != 4 {
		t.Fatalf("got %d phys", len(phys))
	}
	for i, want := range []string{"S", "T", "D", "U"} {
		if routing, _ := phys[i].Routing.Get(); routing != want {
			t.Errorf("phy %d routing %v, want %s", i, phys[i].Routing, want)
		}
	}
	// The rate is the first field after the bracket; a zone group or
	// anything else after it belongs to another column.
	if text, _ := phys[0].Negotiated.Text.Get(); text != "12 Gbps" {
		t.Errorf("phy 0 rate %q", text)
	}
	if gbps, _ := phys[0].Negotiated.Gbps.Get(); gbps != 12 {
		t.Errorf("phy 0 gbps %v", phys[0].Negotiated.Gbps)
	}
	// A duplicate phy line must not produce a second phy: the error log
	// would then be read twice and reported twice for one link.
	twice := parseSMPDiscoverList(smpDiscoverOut+smpDiscoverOut, "source")
	if len(twice) != 4 {
		t.Errorf("duplicate lines produced %d phys", len(twice))
	}
	counters, phy := parseSMPPhyErrorLog(errorLog(7, "long"))
	if n, _ := phy.Get(); n != 7 {
		t.Errorf("phy identifier %v", phy)
	}
	if n, _ := counters.InvalidDword.Get(); n != 70 {
		t.Errorf("invalid dwords %v", counters.InvalidDword)
	}
	if n, _ := counters.RunningDisparityError.Get(); n != 14 {
		t.Errorf("disparity errors %v", counters.RunningDisparityError)
	}
	if n, _ := counters.PhyResetProblem.Get(); n != 0 {
		t.Errorf("reset problems %v", counters.PhyResetProblem)
	}
	empty, _ := parseSMPPhyErrorLog("Report phy error log response:\n  phy identifier: 0\n")
	if empty.Present() {
		t.Errorf("counters were invented from a response that carried none: %+v", empty)
	}
}

// TestSMPFailuresAreGrouped covers what a real six-expander shelf without
// smp_utils produced: one note that repeated the same sentence six times.
//
// The same reason is counted once with the number of expanders behind it,
// and a reason that belongs to a single expander still names it.
func TestSMPFailuresAreGrouped(t *testing.T) {
	t.Parallel()
	missing := "smp_rep_general: not found in /usr/sbin:/usr/bin:/sbin:/bin or PATH (install the smp-utils package)"
	expanders := make([]Expander, 6)
	for i := range expanders {
		expanders[i] = Expander{
			Name: fmt.Sprintf("expander-1:%d", i),
			SMP:  SMPStatus{Requested: true, Err: Some(missing)},
		}
	}
	status := summarizeSMP(expanders)
	reason, ok := status.Err.Get()
	if !ok {
		t.Fatal("six failed expanders produced no reason")
	}
	if strings.Count(reason, missing) != 1 {
		t.Errorf("the reason is repeated:\n%s", reason)
	}
	if !strings.HasPrefix(reason, "6 expanders: ") {
		t.Errorf("the count is missing: %s", reason)
	}
	if status.Available || status.OK {
		t.Errorf("status %+v", status)
	}
	// One expander failing on its own is still named, because then the
	// name is the useful half of the sentence.
	alone := summarizeSMP([]Expander{
		{Name: "expander-1:0", SMP: SMPStatus{Requested: true, OK: true, Available: true, Phys: 4}},
		{Name: "expander-1:1", SMP: SMPStatus{Requested: true, Err: Some("device or resource busy")}},
	})
	single, _ := alone.Err.Get()
	if single != "expander-1:1: device or resource busy" {
		t.Errorf("single failure: %q", single)
	}
	if !alone.OK || alone.Phys != 4 {
		t.Errorf("one expander answered, so the host did: %+v", alone)
	}
}

// TestToolNotFoundReadsTheSameEveryTime covers the other half of that note:
// the lookup is cached, and the first miss used to be worded differently
// from the ones after it.
func TestToolNotFoundReadsTheSameEveryTime(t *testing.T) {
	t.Parallel()
	tools := newTools()
	first, err := tools.path("smp_rep_general")
	if err == nil {
		t.Skipf("smp_utils is installed here (%s)", first)
	}
	_, again := tools.path("smp_rep_general")
	if again == nil {
		t.Fatal("the second lookup found what the first did not")
	}
	if err.Error() != again.Error() {
		t.Errorf("cached miss reads differently:\n%s\n%s", err, again)
	}
	if !strings.Contains(err.Error(), "smp-utils") {
		t.Errorf("the message does not name the package to install: %s", err)
	}
}

// TestSMPDiscoverDescribesFewerPhys is the finding of the second hardware
// run: "smp_discover --multiple" described 24 of an expander's 49 phys and
// said nothing about the rest, so those 25 were never asked for their error
// log — the half of this feature that does not need discover at all.
//
// The phy count the expander itself reports is the authority, and every phy
// in it gets a row and a read.
func TestSMPDiscoverDescribesFewerPhys(t *testing.T) {
	t.Parallel()
	report := smpReport(t, &smpRunner{discover: func() (string, error) {
		return "  phy   0:S:attached:[500605b00b1e2f40:00  i(SSP+STP+SMP)]  12 Gbps\n" +
			"  phy   3:U:attached:[5000cca25de1c2f1:00  t(SATA)]  6 Gbps\n", nil
	}})
	expander := report.Expanders[0]
	if len(expander.Phys) != 4 {
		t.Fatalf("got %d phys, want the 4 the expander reported: %+v", len(expander.Phys), expander.Phys)
	}
	for i, phy := range expander.Phys {
		if phy.Identifier != int64(i) {
			t.Fatalf("phy %d is numbered %d", i, phy.Identifier)
		}
	}
	// The two discover did not describe carry no attached address and say
	// why, and their counters were still read.
	for _, i := range []int{1, 2} {
		phy := expander.Phys[i]
		if phy.AttachedAddress.Present() {
			t.Errorf("phy %d got an address nobody reported: %+v", i, phy)
		}
		if !strings.Contains(phy.Source, "not among the ones it described") {
			t.Errorf("phy %d does not say why it is bare: %q", i, phy.Source)
		}
		if n, ok := phy.Counters.InvalidDword.Get(); !ok || n != int64(i*10) {
			t.Errorf("phy %d counters were not read: %+v", i, phy.Counters)
		}
	}
	if expander.SMP.Phys != 4 || report.Collection.SMP.Phys != 4 {
		t.Errorf("read %d phy error logs, want all 4", expander.SMP.Phys)
	}
	// The described ones keep what discover said about them.
	if address, _ := expander.Phys[3].AttachedAddress.Get(); address != "0x5000cca25de1c2f1" {
		t.Errorf("phy 3 lost its address: %+v", expander.Phys[3])
	}
}
