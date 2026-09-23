package jbod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// shelfPages is what one shelf answers per SES page, so a test can make a
// single page fail or disagree without touching the others.
type shelfPages struct {
	config     string
	status     string
	join       string
	thresholds string
	fail       map[string]error
}

func defaultPages() *shelfPages {
	return &shelfPages{
		config: configPage, status: statusPage, join: joinPage, thresholds: thresholdPage,
		fail: map[string]error{},
	}
}

// inspected builds a client over a small sysfs tree and the page fixtures,
// so the whole of Inspect can be exercised without a shelf.
//
// The sysfs bays are numbered the way the enclosure numbers them, because
// the bay number is what ties an SES element to the disk the kernel sees:
// bay 0 has a disk, bay 1 has one the near module cannot reach, and bay 2
// is empty.
func inspected(t *testing.T, pages *shelfPages) *Client {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "1:0:0:0")
	mkdir(t, base)
	write(t, filepath.Join(base, "id"), "0x5000ccab05629d00\n", 0o444)
	bays := []struct {
		name   string
		number string
		status string
		sg     string
	}{
		{"SLOT 00,T5G93KUD", "0", "OK", "sg1"},
		{"SLOT 01,T5G8WD4D", "1", "OK", "sg2"},
		{"SLOT 02", "2", "not installed", ""},
	}
	for _, bay := range bays {
		dir := filepath.Join(base, bay.name)
		mkdir(t, dir)
		write(t, filepath.Join(dir, "slot"), bay.number+"\n", 0o444)
		write(t, filepath.Join(dir, "type"), "array device\n", 0o444)
		write(t, filepath.Join(dir, "status"), bay.status+"\n", 0o444)
		if bay.sg != "" {
			mkdir(t, filepath.Join(dir, "device", "scsi_generic", bay.sg))
		}
	}
	return New(WithSysfs(root), WithRunner(func(_ context.Context, name string, args ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0] enclosu HGST H4060-J 4013 /dev/sg0\n", nil
		case "sg_inq":
			return "Vendor identification: HGST\nProduct identification: H4060-J\nUnit serial number: ENC1\n", nil
		case "sg_map":
			return "/dev/sg1 /dev/sda\n/dev/sg2 /dev/sdb\n", nil
		case "sg_ses":
			if len(args) == 0 {
				return "", fmt.Errorf("sg_ses without a page")
			}
			if err := pages.fail[args[0]]; err != nil {
				return "", err
			}
			switch args[0] {
			case "--page=cf":
				return pages.config, nil
			case "--page=es":
				return pages.status, nil
			case "--join":
				return pages.join, nil
			case "--page=th":
				// The page is only ever decoded from its raw bytes.
				if !slices.Contains(args, "--raw") {
					return "", fmt.Errorf("the threshold page was read without --raw: %v", args)
				}
				return pages.thresholds, nil
			case "-j":
				// The fan collector, which is unchanged.
				return "FAN ENCL 1 [2,0] Cooling\n", nil
			}
			return "speed code: 2, Actual speed: 7220 rpm, low speed\n", nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	}))
}

// inspectOnly runs one inspection and returns the single shelf's report.
func inspectOnly(t *testing.T, c *Client) EnclosureStatus {
	t.Helper()
	ctx := context.Background()
	enclosures, err := c.Enclosures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := c.Inspect(ctx, enclosures)
	if err != nil {
		// Page failures are tolerated and reported inside the status; a
		// hard error here is a bug in the test setup.
		t.Logf("inspect reported: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses = %d, want 1", len(statuses))
	}
	return statuses[0]
}

func componentsByIndex(status EnclosureStatus) map[string]Component {
	index := map[string]Component{}
	for _, c := range status.Components {
		index[c.Index] = c
	}
	return index
}

func TestInspectComponents(t *testing.T) {
	t.Parallel()
	status := inspectOnly(t, inspected(t, defaultPages()))
	components := componentsByIndex(status)
	// Eleven elements are reported, one of which is the overall cooling
	// element and is not a component; the configuration declares one bay
	// and one power supply more than the status pages reported, and those
	// are listed rather than dropped.
	if _, ok := components["2,-1"]; ok {
		t.Error("the overall cooling element was listed as a component")
	}
	missing, ok := components["0,3"]
	if !ok {
		t.Fatalf("the fourth declared bay is missing from the listing: %v", components)
	}
	if !missing.Declared || missing.Health != HealthUnknown {
		t.Errorf("declared-only bay = %+v", missing)
	}
	if status.Collection.Missing != 1 {
		t.Errorf("missing = %d, want 1", status.Collection.Missing)
	}
	// The conditions the enclosure reports, mapped onto levels. None of
	// these may collapse into "ok".
	for index, want := range map[string]HealthLevel{
		"0,0": HealthOK,
		"0,1": HealthUnknown, // "No access allowed": the far module's bay
		"0,2": HealthAbsent,  // "Not installed": an empty bay
		"1,1": HealthCritical,
		"3,1": HealthUnknown, // "Unsupported"
	} {
		if got := components[index].Health; got != want {
			t.Errorf("component %s health = %q, want %q", index, got, want)
		}
	}
}

// The bay, the address the enclosure reports for it and the disk the kernel
// sees are one thing seen three ways, and the report joins them (ROADMAP 5).
func TestInspectMapsSlotsToDisks(t *testing.T) {
	t.Parallel()
	components := componentsByIndex(inspectOnly(t, inspected(t, defaultPages())))
	bay := components["0,0"]
	if got := bay.Device.Or(""); got != "/dev/sg1" {
		t.Errorf("bay 0 device = %q, want /dev/sg1", got)
	}
	if got := bay.Map.Or(""); got != "/dev/sda" {
		t.Errorf("bay 0 block device = %q, want /dev/sda", got)
	}
	if len(bay.SASAddresses) != 1 || bay.SASAddresses[0] != "0x5000cca2a0d6e2f5" {
		t.Errorf("bay 0 addresses = %v", bay.SASAddresses)
	}
	// A bay the near module cannot reach still has a disk in sysfs, and the
	// mapping is what says so.
	if got := components["0,1"].Device.Or(""); got != "/dev/sg2" {
		t.Errorf("bay 1 device = %q, want /dev/sg2", got)
	}
	// An element that is not a bay is never given a disk.
	if psu := components["1,0"]; psu.Device.Present() || psu.Map.Present() {
		t.Errorf("a power supply was mapped to a disk: %+v", psu)
	}
}

func TestInspectReadings(t *testing.T) {
	t.Parallel()
	components := componentsByIndex(inspectOnly(t, inspected(t, defaultPages())))
	sensor, ok := components["3,0"].Reading(ReadingTemperature)
	if !ok {
		t.Fatalf("the temperature sensor reports no reading: %+v", components["3,0"])
	}
	if v, present := sensor.Value.Get(); !present || v != 35 {
		t.Errorf("temperature = %v, want 35", sensor.Value)
	}
	if sensor.Unit != UnitCelsius || sensor.Source == "" || sensor.ReadAt.IsZero() {
		t.Errorf("reading is missing its unit, source or time: %+v", sensor)
	}
	if sensor.Thresholds == nil {
		t.Fatal("the sensor has no thresholds")
	}
	if v, present := sensor.Thresholds.HighCritical.Get(); !present || v != 65 {
		t.Errorf("high critical = %v, want 65", sensor.Thresholds.HighCritical)
	}
	// A sensor that declares a reading and reported none keeps the reading
	// with its value absent: a dropped row is indistinguishable from a
	// sensor that never existed, and a zero is a lie.
	unread, ok := components["3,1"].Reading(ReadingTemperature)
	if !ok {
		t.Fatalf("the unreadable sensor lost its reading: %+v", components["3,1"])
	}
	if unread.Value.Present() {
		t.Errorf("unreadable sensor reported %v", unread.Value)
	}
	if !unread.Err.Present() {
		t.Error("the unreadable sensor has no reason for the absent value")
	}
	// A power supply reports a temperature too, and it is a reading.
	if _, ok := components["1,0"].Reading(ReadingTemperature); !ok {
		t.Error("the power supply temperature was dropped")
	}
	if speed, ok := components["2,0"].Reading(ReadingSpeed); !ok || speed.Unit != UnitRPM {
		t.Errorf("cooling element reading = %+v", speed)
	}
}

func TestInspectHealth(t *testing.T) {
	t.Parallel()
	status := inspectOnly(t, inspected(t, defaultPages()))
	// The enclosure's own verdict and the verdict of its elements are two
	// answers to two questions, and the report keeps them apart.
	if status.Hardware.Level != HealthWarning {
		t.Errorf("hardware level = %q, want warning", status.Hardware.Level)
	}
	if status.Summary.Level != HealthCritical {
		t.Errorf("components level = %q, want critical", status.Summary.Level)
	}
	if status.Level() != HealthCritical {
		t.Errorf("overall level = %q, want critical", status.Level())
	}
	// Unknown and absent elements are counted rather than folded into the
	// verdict, which is where an operator sees that part of the shelf was
	// not readable.
	if n := status.Summary.Count(HealthUnknown); n == 0 {
		t.Errorf("no unknown elements were counted: %v", status.Summary.Counts)
	}
	if n := status.Summary.Count(HealthAbsent); n != 1 {
		t.Errorf("absent elements = %d, want 1", n)
	}
	if !status.Collection.Complete {
		t.Errorf("collection is not complete: %+v", status.Collection)
	}
}

// A page that did not answer is a gap in the poll. It must not become a
// healthy enclosure, and it must not take the rest of the report with it.
func TestInspectPartialPoll(t *testing.T) {
	t.Parallel()
	pages := defaultPages()
	pages.fail["--page=es"] = fmt.Errorf("sg_ses: bad field in cdb")
	status := inspectOnly(t, inspected(t, pages))
	if status.Hardware.Level != HealthUnknown {
		t.Errorf("hardware level = %q, want unknown", status.Hardware.Level)
	}
	if status.Hardware.Critical.Present() {
		t.Errorf("a page that did not answer reported a condition bit: %+v", status.Hardware)
	}
	if status.Collection.Complete {
		t.Error("a failed required page left the collection marked complete")
	}
	// The elements are still there: one page is not the whole shelf.
	if len(status.Components) == 0 {
		t.Error("a failed status page took the component listing with it")
	}
	if _, ok := componentsByIndex(status)["3,0"]; !ok {
		t.Error("the sensors were lost with the status page")
	}
}

// An enclosure that does not implement the Threshold In page is not an
// enclosure we failed to read.
func TestInspectThresholdPageOptional(t *testing.T) {
	t.Parallel()
	pages := defaultPages()
	pages.fail["--page=th"] = fmt.Errorf("sg_ses: Threshold In dpage not supported")
	status := inspectOnly(t, inspected(t, pages))
	if !status.Collection.Complete {
		t.Errorf("an unsupported optional page made the collection incomplete: %+v", status.Collection)
	}
	sensor, ok := componentsByIndex(status)["3,0"].Reading(ReadingTemperature)
	if !ok {
		t.Fatal("the sensor lost its reading")
	}
	if v, present := sensor.Value.Get(); !present || v != 35 {
		t.Errorf("temperature = %v, want 35", sensor.Value)
	}
	if sensor.Thresholds != nil {
		t.Errorf("thresholds were invented for a page that is not supported: %+v", sensor.Thresholds)
	}
}

// Pages that report different generation codes describe different
// configurations, so the report says so instead of presenting the mixture
// as one shelf.
func TestInspectGenerationChanged(t *testing.T) {
	t.Parallel()
	pages := defaultPages()
	pages.status = "Enclosure Status diagnostic page:\n  INVOP=0, INFO=0, NON-CRIT=0, CRIT=0, UNRECOV=0\n  generation code: 0x2\n"
	status := inspectOnly(t, inspected(t, pages))
	if !status.Collection.GenerationChanged {
		t.Errorf("a generation change went unreported: %+v", status.Collection)
	}
	if status.Collection.Complete {
		t.Error("a mixed report was marked complete")
	}
	// The pass is not repeated, so the readings that were taken are still
	// reported; they are simply marked as a mixture.
	if len(status.Components) == 0 {
		t.Error("the components were dropped over a generation change")
	}
}

// The collection counts the pages that failed, so the exporter can publish
// them, and the CLI can tell a shelf in trouble from a shelf it could not
// read.
func TestInspectCountsPageFailures(t *testing.T) {
	t.Parallel()
	pages := defaultPages()
	pages.fail["--join"] = fmt.Errorf("sg_ses: device busy")
	c := inspected(t, pages)
	snapshot, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Errors[CollectorComponents] == 0 {
		t.Errorf("a failed page was not counted: %v", snapshot.Errors)
	}
	if len(snapshot.Status) != 1 {
		t.Fatalf("snapshot has %d shelf reports", len(snapshot.Status))
	}
	if snapshot.ReadAt.IsZero() {
		t.Error("the snapshot has no collection time")
	}
	// The disks and the fans are collected by other code paths and must
	// survive a page that did not answer.
	if len(snapshot.Slots) == 0 {
		t.Error("the slot walk was lost with the SES page")
	}
}

// Inspect walks sysfs itself, so the command-line reports do not depend on
// the caller having walked it first.
func TestInspectWithoutEnclosures(t *testing.T) {
	t.Parallel()
	c := inspected(t, defaultPages())
	statuses, err := c.Inspect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 0 {
		t.Errorf("statuses = %d, want 0", len(statuses))
	}
}

func TestWorstOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   []HealthLevel
		want HealthLevel
	}{
		{"nothing observed", nil, HealthUnknown},
		{"only unknown", []HealthLevel{HealthUnknown, HealthAbsent}, HealthUnknown},
		{"ok wins over absent", []HealthLevel{HealthAbsent, HealthOK}, HealthOK},
		{"warning over ok", []HealthLevel{HealthOK, HealthWarning}, HealthWarning},
		{"critical over warning", []HealthLevel{HealthWarning, HealthCritical, HealthOK}, HealthCritical},
		{"unrecoverable is worst", []HealthLevel{HealthCritical, HealthUnrecoverable}, HealthUnrecoverable},
		// An unknown element must not mask a critical one, and must not be
		// ranked above it either.
		{"unknown does not mask critical", []HealthLevel{HealthUnknown, HealthCritical}, HealthCritical},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := worstOf(c.in); got != c.want {
				t.Errorf("worstOf(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestHealthOfStatus(t *testing.T) {
	t.Parallel()
	cases := map[string]HealthLevel{
		"OK":                HealthOK,
		"ok":                HealthOK,
		"Noncritical":       HealthWarning,
		"Non-critical":      HealthWarning,
		"Critical":          HealthCritical,
		"Unrecoverable":     HealthUnrecoverable,
		"Not installed":     HealthAbsent,
		"Unsupported":       HealthUnknown,
		"Unknown":           HealthUnknown,
		"Not available":     HealthUnknown,
		"No access allowed": HealthUnknown,
		"":                  HealthUnknown,
		"something new":     HealthUnknown,
	}
	for status, want := range cases {
		if got := healthOfStatus(status); got != want {
			t.Errorf("healthOfStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

// A shelf whose sysfs tree cannot be read still produces a report: the
// components come from the SES pages and do not depend on it.
func TestInspectWithoutSysfs(t *testing.T) {
	t.Parallel()
	c := inspected(t, defaultPages()).With(WithSysfs(filepath.Join(t.TempDir(), "absent")))
	ctx := context.Background()
	enclosures, err := c.Enclosures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	statuses, _ := c.Inspect(ctx, enclosures)
	if len(statuses) != 1 {
		t.Fatalf("statuses = %d, want 1", len(statuses))
	}
	if len(statuses[0].Components) == 0 {
		t.Error("the components were lost with the sysfs tree")
	}
	if statuses[0].Components[0].Device.Present() {
		t.Error("a disk was reported without a sysfs tree to read it from")
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("the fixture created the directory it was meant to lack")
	}
}

// The bay number the enclosure reports is the enclosure's own answer to
// "which bay is this". When sysfs does not have that bay, the honest result
// is no disk: matching on the element ordinal instead would attach the
// wrong disk to the right bay on a chassis whose element and bay numbering
// are offset.
func TestJoinBayDoesNotGuessPastTheBayNumber(t *testing.T) {
	t.Parallel()
	slots := []Slot{
		{Enclosure: "1:0:0:0", Name: "SLOT 00", Label: "SLOT 00", Number: Some(int64(0)),
			Device: Some("/dev/sg1"), Map: Some("/dev/sda")},
		{Enclosure: "1:0:0:0", Name: "SLOT 01", Label: "SLOT 01", Number: Some(int64(1)),
			Device: Some("/dev/sg2"), Map: Some("/dev/sdb")},
	}
	bays := baysOf(slots, "1:0:0:0")
	// The enclosure calls this bay 30; sysfs has no bay 30.
	far := Component{Index: "0,1", Element: 1, Type: "array device slot", SlotNumber: Some(int64(30))}
	joinBay(&far, bays)
	if far.Device.Present() || far.Map.Present() {
		t.Errorf("bay 30 was given the disk of another bay: %+v", far)
	}
	if n := far.SlotNumber.Or(-1); n != 30 {
		t.Errorf("the bay number was rewritten to %d", n)
	}
	// An enclosure that reports no bay number falls back to the element
	// number, and then to the descriptor.
	byElement := Component{Index: "0,1", Element: 1, Type: "array device slot"}
	joinBay(&byElement, bays)
	if got := byElement.Device.Or(""); got != "/dev/sg2" {
		t.Errorf("fallback by element number = %q, want /dev/sg2", got)
	}
	byName := Component{Index: "0,9", Element: 9, Type: "array device slot", Name: "SLOT 00"}
	joinBay(&byName, bays)
	if got := byName.Device.Or(""); got != "/dev/sg1" {
		t.Errorf("fallback by descriptor = %q, want /dev/sg1", got)
	}
}

// A headline of "ok" is a claim that the shelf is fine, and a shelf whose
// status page did not answer has not made that claim.
func TestLevelDoesNotCallAnUnreadShelfHealthy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name               string
		hardware, elements HealthLevel
		want               HealthLevel
	}{
		{"both answered", HealthOK, HealthOK, HealthOK},
		{"hardware unknown", HealthUnknown, HealthOK, HealthUnknown},
		{"elements unknown", HealthOK, HealthUnknown, HealthUnknown},
		// Unknown still never masks a real fault.
		{"unknown does not mask critical", HealthUnknown, HealthCritical, HealthCritical},
		{"hardware warns", HealthWarning, HealthOK, HealthWarning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			status := EnclosureStatus{
				Hardware: HardwareStatus{Level: c.hardware},
				Summary:  ComponentSummary{Level: c.elements},
			}
			if got := status.Level(); got != c.want {
				t.Errorf("Level() = %q, want %q", got, c.want)
			}
		})
	}
}

// End to end: a status page that did not answer must not produce a healthy
// headline for a shelf whose elements all read OK.
func TestInspectUnreadableStatusPageIsNotHealthy(t *testing.T) {
	t.Parallel()
	pages := defaultPages()
	pages.fail["--page=es"] = fmt.Errorf("sg_ses: bad field in cdb")
	// Every element reads OK, so only the missing page is left to notice.
	pages.join = "PSU A [1,0]  Element type: Power supply\n      status: OK\n"
	status := inspectOnly(t, inspected(t, pages))
	if status.Summary.Level != HealthOK {
		t.Fatalf("components level = %q, want ok", status.Summary.Level)
	}
	if got := status.Level(); got != HealthUnknown {
		t.Errorf("headline = %q, want unknown for a shelf whose status page did not answer", got)
	}
}

// A threshold page that answered and could not be decoded is a failure of
// this collection, not of the shelf: the sensors keep their readings, get
// no limits, the reason travels with the page and the failure is counted.
// The output here is what sg_ses prints without --raw, which is the thing a
// decoder that trusted the text would have had to read.
func TestInspectThresholdPageUndecoded(t *testing.T) {
	t.Parallel()
	pages := defaultPages()
	pages.thresholds = `Threshold In diagnostic page:
  INVOP=0
  generation code: 0x1
  Threshold status descriptor list
    Element type: Temperature sensor, subenclosure id: 0 [ti=3]
      Overall descriptor:
        high critical=100, high warning=95
        low warning=0, low critical=-19 (in Celsius)
`
	snapshot, err := inspected(t, pages).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Errors[CollectorComponents] == 0 {
		t.Errorf("an undecoded page was not counted: %v", snapshot.Errors)
	}
	status := snapshot.Status[0]
	sensor, ok := componentsByIndex(status)["3,0"].Reading(ReadingTemperature)
	if !ok || sensor.Value.Or(0) != 35 {
		t.Fatalf("the sensor lost its reading: %+v", sensor)
	}
	if sensor.Thresholds != nil {
		t.Errorf("limits were attached from a page that was not decoded: %+v", sensor.Thresholds)
	}
	page := status.Collection.Pages[3]
	if !page.OK || !strings.Contains(page.Err.Or(""), "was not decoded") {
		t.Errorf("threshold page status %+v, want answered with the reason it was not used", page)
	}
	// The page is optional, so the collection is still complete.
	if !status.Collection.Complete {
		t.Errorf("an optional page made the collection incomplete: %+v", status.Collection)
	}
}

// The threshold page takes part in the generation check with the code in
// its raw bytes: a configuration that changed between the pages of one pass
// is reported, and its limits are not attached.
func TestInspectThresholdPageGeneration(t *testing.T) {
	t.Parallel()
	pages := defaultPages()
	pages.thresholds = "00 00 00 02" + strings.TrimPrefix(thresholdPage, "00 00 00 01")
	status := inspectOnly(t, inspected(t, pages))
	if !status.Collection.GenerationChanged || status.Collection.Complete {
		t.Errorf("a threshold page of another generation went unnoticed: %+v", status.Collection)
	}
	if got := status.Collection.Pages[3].Generation.Or(""); got != "0x2" {
		t.Errorf("threshold page generation %q, want 0x2", got)
	}
	sensor, _ := componentsByIndex(status)["3,0"].Reading(ReadingTemperature)
	if sensor.Thresholds != nil {
		t.Errorf("limits of another configuration were attached: %+v", sensor.Thresholds)
	}
}
