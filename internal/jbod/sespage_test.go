package jbod

import (
	"fmt"
	"strings"
	"testing"
)

// The fixtures below are the shapes sg_ses has been observed to print, with
// the fields a 60-bay two-module shelf reports: a primary and a secondary
// subenclosure, sixty bays, eight cooling elements, two power supplies and
// a handful of sensors.
//
// They are deliberately not uniform. A parser that only ever sees tidy
// output is a parser that turns the first unusual line into a zero.

const configPage = `Configuration diagnostic page:
  number of secondary subenclosures: 1
  generation code: 0x1
  enclosure descriptor list
    Subenclosure identifier: 0 [primary]
      relative ES process id: 1, number of ES processes: 1
      number of type descriptor headers: 5
      enclosure logical identifier (hex): 5000ccab05629d00
      enclosure vendor: HGST      product: H4060-J      rev: 4013
    Subenclosure identifier: 1
      number of type descriptor headers: 1
      enclosure logical identifier (hex): 5000ccab05629d40
  type descriptor header and text list
    Element type: Array device slot, subenclosure id: 0, number of possible elements: 4
      text: SLOT
    Element type: Power supply, subenclosure id: 0, number of possible elements: 2
      text: PSU
    Element type: Cooling, subenclosure id: 0
      number of possible elements: 2
      text: FAN
    Element type: Temperature sensor, subenclosure id: 0, number of possible elements: 2
      text: TEMP
    Element type: Voltage sensor, subenclosure id: 1, number of possible elements: 1
      text: VOLT
`

const statusPage = `Enclosure Status diagnostic page:
  INVOP=0, INFO=0, NON-CRIT=1, CRIT=0, UNRECOV=0
  generation code: 0x1
  status descriptor list
`

// joinPage is the shape that carries the type on every element line, with
// the Additional Element Status of the bays underneath them.
const joinPage = `  Primary enclosure logical identifier (hex): 5000ccab05629d00
SLOT 00,T5G93KUD [0,0]  Element type: Array device slot
      Predicted failure=0, Disabled=0, Swap=0, status: OK
      App client bypassed A=0, Do not remove=0, Enc bypassed A=0, Enc bypassed B=0
      transport protocol: SAS
        number of phys: 1, not all phys: 0, device slot number: 0
        phy index: 0
          SAS device type: end device
          attached SAS address: 0x5000ccab05629d3d
          SAS address: 0x5000cca2a0d6e2f5
          phy identifier: 0x0
SLOT 01,T5G8WD4D [0,1]  Element type: Array device slot
      Predicted failure=0, Disabled=0, Swap=0, status: No access allowed
      transport protocol: SAS
        number of phys: 1, not all phys: 0, device slot number: 1
        phy index: 0
          SAS address: 0x0000000000000000
SLOT 02 [0,2]  Element type: Array device slot
      Predicted failure=0, Disabled=0, Swap=0, status: Not installed
PSU A [1,0]  Element type: Power supply
      Predicted failure=0, Disabled=0, Swap=0, status: OK
      Hot swap=1, Fail=0, Requested on=1, Off=0, Overtmp fail=0, Temperature warn=0
      AC fail=0, DC fail=0
      Temperature=41 C
PSU B [1,1]  Element type: Power supply
      Predicted failure=0, Disabled=0, Swap=0, status: Critical
      Hot swap=1, Fail=1, Requested on=1, Off=0, Overtmp fail=0, Temperature warn=0
      AC fail=1, DC fail=0
FAN ENCL 1 [2,0]  Element type: Cooling
      Predicted failure=0, Disabled=0, Swap=0, status: OK
      Fail=0, Requested on=1, Off=0, Actual speed=7220 rpm, Fan at third lowest speed
FAN ENCL 2 [2,1]  Element type: Cooling
      Predicted failure=0, Disabled=0, Swap=0, status: OK
      Fail=0, Requested on=1, Off=0, Actual speed=0 rpm, Fan stopped
 [2,-1]  Element type: Cooling
      Predicted failure=0, Disabled=0, Swap=0, status: OK
      Fail=0, Requested on=1, Off=0, Actual speed=0 rpm, Fan stopped
TEMP IOM A [3,0]  Element type: Temperature sensor
      Predicted failure=0, Disabled=0, Swap=0, status: OK
      Ident=0, Fail=0, OT failure=0, OT warning=0, UT failure=0, UT warning=0
      Temperature=35 C
TEMP IOM B [3,1]  Element type: Temperature sensor
      Predicted failure=0, Disabled=0, Swap=0, status: Unsupported
      Ident=0, Fail=0, OT failure=0, OT warning=0, UT failure=0, UT warning=0
      Temperature: <reserved>
VOLT 12V [4,0]  Element type: Voltage sensor
      Predicted failure=0, Disabled=0, Swap=0, status: OK
      Warn over=0, Warn under=0, Crit over=0, Crit under=0
      Voltage: 12.01 volts
`

// thresholdPage is "sg_ses --page=th --raw" for configPage: the page from
// its generation code on, in the layout sg_ses prints raw bytes in. Every
// element of every type has a descriptor, the bays, supplies and fans
// included, which is what a decoder that skipped them would misread.
//
//	[3,-1] temperature overall  78 73 14 01  100 95 0 -19 C
//	[3,0]  temperature          55 50 14 01   65 60 0 -19 C
//	[3,1]  temperature          55 50 14 00   65 60 0, low critical not supported
//	[4,-1] voltage overall      00 00 00 00  none declared
//	[4,0]  voltage              0a 06 06 0a  5.0 3.0 3.0 5.0 % of nominal
const thresholdPage = `00 00 00 01 00 00 00 00  00 00 00 00 00 00 00 00
00 00 00 00 00 00 00 00  00 00 00 00 00 00 00 00
00 00 00 00 00 00 00 00  00 00 00 00 00 00 00 00
78 73 14 01 55 50 14 01  55 50 14 00 00 00 00 00
0a 06 06 0a
`

func TestParseConfiguration(t *testing.T) {
	t.Parallel()
	cfg := parseConfiguration(configPage)
	if got, ok := cfg.Generation.Get(); !ok || got != "0x1" {
		t.Errorf("generation = %v, want 0x1", cfg.Generation)
	}
	if len(cfg.Subenclosures) != 2 {
		t.Fatalf("subenclosures = %d, want 2", len(cfg.Subenclosures))
	}
	primary := cfg.Subenclosures[0]
	if !primary.Primary {
		t.Error("the first subenclosure is not marked primary")
	}
	if got := primary.LogicalID.Or(""); got != "5000ccab05629d00" {
		t.Errorf("logical id = %q", got)
	}
	// The vendor line puts three fields on one line, separated by runs of
	// spaces; taking the rest of the line would make the vendor "HGST
	// product: H4060-J rev: 4013".
	if got := primary.Vendor.Or(""); got != "HGST" {
		t.Errorf("vendor = %q, want HGST", got)
	}
	if got := primary.Product.Or(""); got != "H4060-J" {
		t.Errorf("product = %q, want H4060-J", got)
	}
	if got := primary.Revision.Or(""); got != "4013" {
		t.Errorf("revision = %q, want 4013", got)
	}
	if len(cfg.Types) != 5 {
		t.Fatalf("types = %d, want 5", len(cfg.Types))
	}
	// The type index is the position in this list, which is what every
	// other page addresses an element by.
	for i, want := range []struct {
		kind     string
		possible int64
		sub      int64
	}{
		{"array device slot", 4, 0},
		{"power supply", 2, 0},
		{"cooling", 2, 0},
		{"temperature sensor", 2, 0},
		{"voltage sensor", 1, 1},
	} {
		got := cfg.Types[i]
		if got.TypeIndex != int64(i) {
			t.Errorf("type %d has index %d", i, got.TypeIndex)
		}
		if got.Type != want.kind {
			t.Errorf("type %d = %q, want %q", i, got.Type, want.kind)
		}
		// "number of possible elements" is on the type line for some
		// entries and on its own line for others; both have to work.
		if n, ok := got.Possible.Get(); !ok || n != want.possible {
			t.Errorf("type %d possible = %v, want %d", i, got.Possible, want.possible)
		}
		if n, ok := got.Subenclosure.Get(); !ok || n != want.sub {
			t.Errorf("type %d subenclosure = %v, want %d", i, got.Subenclosure, want.sub)
		}
	}
}

func TestParseEnclosureStatus(t *testing.T) {
	t.Parallel()
	status := parseEnclosureStatus(statusPage)
	if got, ok := status.Generation.Get(); !ok || got != "0x1" {
		t.Errorf("generation = %v", status.Generation)
	}
	// NON-CRIT must not be read out of CRIT, nor CRIT out of NON-CRIT: the
	// two bits mean different things and sit on the same line.
	if v, ok := status.NonCritical.Get(); !ok || !v {
		t.Errorf("NON-CRIT = %v, want true", status.NonCritical)
	}
	if v, ok := status.Critical.Get(); !ok || v {
		t.Errorf("CRIT = %v, want false", status.Critical)
	}
	if v, ok := status.Info.Get(); !ok || v {
		t.Errorf("INFO = %v, want false", status.Info)
	}
	if v, ok := status.Unrecoverable.Get(); !ok || v {
		t.Errorf("UNRECOV = %v, want false", status.Unrecoverable)
	}
}

// A page that did not answer must leave every bit absent. Five zeros is a
// healthy enclosure, and reporting one for a page nobody could read is the
// failure this model exists to prevent.
func TestParseEnclosureStatusEmpty(t *testing.T) {
	t.Parallel()
	status := parseEnclosureStatus("")
	for name, value := range map[string]Optional[bool]{
		"INVOP": status.InvalidOperation, "INFO": status.Info, "NON-CRIT": status.NonCritical,
		"CRIT": status.Critical, "UNRECOV": status.Unrecoverable,
	} {
		if value.Present() {
			t.Errorf("%s = %v, want absent", name, value)
		}
	}
}

func byIndex(elements []sesElement) map[string]sesElement {
	index := map[string]sesElement{}
	for _, e := range elements {
		index[e.Index] = e
	}
	return index
}

func TestParseJoinElements(t *testing.T) {
	t.Parallel()
	elements := byIndex(parseJoinElements(joinPage))
	if len(elements) != 11 {
		t.Fatalf("elements = %d, want 11", len(elements))
	}
	bay := elements["0,0"]
	if bay.Type != "array device slot" {
		t.Errorf("bay type = %q", bay.Type)
	}
	if bay.Descriptor != "SLOT 00,T5G93KUD" {
		t.Errorf("bay descriptor = %q", bay.Descriptor)
	}
	if got := bay.Status.Or(""); got != "OK" {
		t.Errorf("bay status = %q", got)
	}
	// The address of the disk, not the address of the expander phy it is
	// attached to: reporting the attached one would map every bay of a
	// shelf to the same device.
	if len(bay.SASAddresses) != 1 || bay.SASAddresses[0] != "0x5000cca2a0d6e2f5" {
		t.Errorf("bay SAS addresses = %v", bay.SASAddresses)
	}
	if n, ok := bay.SlotNumber.Get(); !ok || n != 0 {
		t.Errorf("bay slot number = %v", bay.SlotNumber)
	}
	// A phy that is not connected reports the null address, which is not an
	// identity and must not be published as one.
	if addresses := elements["0,1"].SASAddresses; len(addresses) != 0 {
		t.Errorf("unconnected phy reported %v", addresses)
	}
	if got := elements["0,1"].Status.Or(""); got != "No access allowed" {
		t.Errorf("far-side bay status = %q", got)
	}
	psu := elements["1,1"]
	if !psu.Flags["AC fail"] || !psu.Flags["Fail"] {
		t.Errorf("failed PSU flags = %v", psu.Flags)
	}
	if psu.Flags["DC fail"] {
		t.Error("DC fail is set on a PSU that reports it clear")
	}
	if n, ok := elements["1,0"].Temperature.Get(); !ok || n != 41 {
		t.Errorf("PSU temperature = %v, want 41", elements["1,0"].Temperature)
	}
	if n, ok := elements["2,0"].Speed.Get(); !ok || n != 7220 {
		t.Errorf("fan speed = %v, want 7220", elements["2,0"].Speed)
	}
	// The overall element of a type is parsed but marked, so the layer
	// above can leave it out of the listing instead of publishing a
	// summary as a stopped fan.
	if overall, ok := elements["2,-1"]; !ok || !overall.Overall() {
		t.Errorf("overall cooling element = %+v", overall)
	}
	if n, ok := elements["3,0"].Temperature.Get(); !ok || n != 35 {
		t.Errorf("sensor temperature = %v, want 35", elements["3,0"].Temperature)
	}
	// "Temperature: <reserved>" is not a reading, and "Temperature warn=0"
	// is not one either.
	if got := elements["3,1"].Temperature; got.Present() {
		t.Errorf("unreadable sensor reported %v", got)
	}
	if got := elements["1,0"].Voltage; got.Present() {
		t.Errorf("DC overvoltage flag was read as a voltage: %v", got)
	}
	if v, ok := elements["4,0"].Voltage.Get(); !ok || v != 12.01 {
		t.Errorf("voltage = %v, want 12.01", elements["4,0"].Voltage)
	}
}

// sg_ses has printed the element type in more than one shape. All of them
// carry the same word, and a parser that only knows one of them reports an
// enclosure with no cooling elements on the version that prints another.
func TestParseJoinElementShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{
			name: "type on the element line",
			in: "FAN A [2,0]  Element type: Cooling\n" +
				"      status: OK\n      Actual speed=1200 rpm, low speed\n",
		},
		{
			name: "type after the index",
			in: "FAN A [2,0]  Cooling element\n" +
				"      status: OK\n      Actual speed=1200 rpm, low speed\n",
		},
		{
			name: "type on its own header line",
			in: "  Element type: Cooling, subenclosure id: 0 [ti=2]\n" +
				"    Element 0 descriptor: FAN A\n" +
				"      status: OK\n      Actual speed=1200 rpm, low speed\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			elements := parseJoinElements(c.in)
			if len(elements) != 1 {
				t.Fatalf("elements = %d, want 1: %+v", len(elements), elements)
			}
			got := elements[0]
			if got.Type != "cooling" {
				t.Errorf("type = %q, want cooling", got.Type)
			}
			if got.Index != "2,0" {
				t.Errorf("index = %q, want 2,0", got.Index)
			}
			if n, ok := got.Speed.Get(); !ok || n != 1200 {
				t.Errorf("speed = %v, want 1200", got.Speed)
			}
			if status := got.Status.Or(""); status != "OK" {
				t.Errorf("status = %q", status)
			}
		})
	}
}

// Whatever the input, the parser must not invent elements or values.
func TestParseJoinElementsGarbage(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"", "\n\n", "sg_ses: unable to open /dev/sg0\n",
		"[not,an,index] Element type: Cooling\n",
		"Element type: Cooling\n  Element 0 descriptor:\n",
	} {
		for _, e := range parseJoinElements(in) {
			if e.Temperature.Present() || e.Speed.Present() || e.Voltage.Present() || e.Current.Present() {
				t.Errorf("%q produced a reading: %+v", in, e)
			}
		}
	}
}

func TestNormalizeElementType(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Array device slot":   "array device slot",
		"  Cooling element ":  "cooling",
		"COOLING ELEMENTS":    "cooling",
		"Temperature  sensor": "temperature sensor",
	}
	for in, want := range cases {
		if got := normalizeElementType(in); got != want {
			t.Errorf("normalizeElementType(%q) = %q, want %q", in, got, want)
		}
	}
	// "array device slot" must win over "device slot", which is a prefix
	// match away and a different element type.
	if got, ok := elementTypeIn("Element type: Array device slot"); !ok || got != "array device slot" {
		t.Errorf("elementTypeIn = %q, %v", got, ok)
	}
}

// A type sg_ses cannot name is still a type. sg_ses prints "vendor specific
// [0x81]" for the vendor range, and shelves do declare such elements; an
// unnamed header that left the previous type in place would file its
// elements under the previous type index, which is another element's
// address.
func TestParseJoinElementsUnknownType(t *testing.T) {
	t.Parallel()
	const page = `  Element type: Cooling, subenclosure id: 0 [ti=0]
    Element 0 descriptor: FAN 1
      status: OK
      Actual speed=7220 rpm, Fan at third lowest speed
  Element type: vendor specific [0x81], subenclosure id: 0 [ti=1]
    Element 0 descriptor: VS A
      status: Critical
  Element type: Temperature sensor, subenclosure id: 0 [ti=2]
    Element 0 descriptor: TEMP A
      status: OK
      Temperature=35 C
`
	elements := byIndex(parseJoinElements(page))
	if len(elements) != 3 {
		t.Fatalf("elements = %d, want 3: %+v", len(elements), elements)
	}
	if got := elements["0,0"]; got.Type != "cooling" || got.Status.Or("") != "OK" {
		t.Errorf("cooling element = %+v", got)
	}
	// The vendor element keeps its own index, its own type and its own
	// condition; it does not become a second element 0,0 typed cooling,
	// with a speed reading and a critical status.
	vendor, ok := elements["1,0"]
	if !ok {
		t.Fatalf("the vendor element is missing: %+v", elements)
	}
	if vendor.Type == "cooling" {
		t.Errorf("the vendor element inherited the previous type: %+v", vendor)
	}
	if vendor.Speed.Present() {
		t.Errorf("the vendor element was given a speed: %v", vendor.Speed)
	}
	if got := elements["2,0"]; got.Type != "temperature sensor" {
		t.Errorf("the type after the unnamed one = %q", got.Type)
	}
}

// The generation code ends at the end of its line. A value that swallowed
// the next line would compare unequal to the other pages' and report a
// configuration change that did not happen.
func TestPageGeneration(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"indented next line": "  generation code: 0x2\n  status descriptor list\n",
		"flush next line":    "  generation code: 0x2\nstatus descriptor list\n",
		"last line":          "  generation code: 0x2\n",
	}
	for name, page := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := pageGeneration(page); got.Or("") != "0x2" {
				t.Errorf("generation = %q, want 0x2", got.Or(""))
			}
		})
	}
	if got := pageGeneration("sg_ses: unable to open /dev/sg0\n"); got.Present() {
		t.Errorf("a failed page reported generation %v", got)
	}
}

// rawThresholds renders a Threshold In page the way "sg_ses --raw" prints
// it, so a test can state descriptors instead of hex.
func rawThresholds(generation uint32, descriptors ...[4]byte) string {
	raw := []byte{byte(generation >> 24), byte(generation >> 16), byte(generation >> 8), byte(generation)}
	for _, d := range descriptors {
		raw = append(raw, d[:]...)
	}
	var b strings.Builder
	for i, x := range raw {
		switch {
		case i%16 == 0 && i > 0:
			b.WriteString("\n")
		case i%16 == 8:
			b.WriteString("  ")
		case i%16 != 0:
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%02x", x)
	}
	b.WriteString("\n")
	return b.String()
}

func TestParseThresholdPage(t *testing.T) {
	t.Parallel()
	page, err := parseThresholdPage(thresholdPage, parseConfiguration(configPage))
	if err != nil {
		t.Fatal(err)
	}
	if page.Generation != "0x1" {
		t.Errorf("generation = %q, want 0x1, the spelling of the text pages", page.Generation)
	}
	sensor, ok := page.Limits["3,0"]
	if !ok {
		t.Fatalf("no thresholds for the first temperature sensor: %v", page.Limits)
	}
	for name, got := range map[string]Optional[float64]{
		"high critical": sensor.HighCritical, "high warning": sensor.HighWarning,
		"low warning": sensor.LowWarning, "low critical": sensor.LowCritical,
	} {
		want := map[string]float64{"high critical": 65, "high warning": 60, "low warning": 0, "low critical": -19}[name]
		if v, ok := got.Get(); !ok || v != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if sensor.Unit != UnitCelsius {
		t.Errorf("temperature thresholds in %q", sensor.Unit)
	}
	// 00h is "not supported", not -20 C.
	if second := page.Limits["3,1"]; second.LowCritical.Present() {
		t.Errorf("an unsupported limit was decoded as %v", second.LowCritical)
	}
	// The overall element of a type is not any element of it, so it is kept
	// apart and never attached to one.
	if overall, ok := page.Limits["3,-1"]; !ok || overall.HighCritical.Or(0) != 100 {
		t.Errorf("overall thresholds %+v (present %v), want high critical 100", overall, ok)
	}
	// A voltage limit is a percentage of nominal, not volts.
	volts, ok := page.Limits["4,0"]
	if !ok {
		t.Fatalf("no thresholds for the voltage sensor: %v", page.Limits)
	}
	if v, _ := volts.HighCritical.Get(); v != 5 || volts.Unit != UnitPercentOfNominal {
		t.Errorf("voltage high critical %v %s, want 5 %s", volts.HighCritical, volts.Unit, UnitPercentOfNominal)
	}
	if v, _ := volts.LowWarning.Get(); v != 3 {
		t.Errorf("voltage low warning %v, want 3", volts.LowWarning)
	}
	// Elements without thresholds and a type's all-zero overall descriptor
	// produce nothing.
	for _, key := range []string{"0,0", "1,0", "2,0", "4,-1"} {
		if _, ok := page.Limits[key]; ok {
			t.Errorf("%s has thresholds it does not declare", key)
		}
	}
}

// A current sensor has high limits only; its low fields are reserved, and a
// reserved field that happens to be set is not a limit.
func TestParseThresholdPageCurrent(t *testing.T) {
	t.Parallel()
	cfg := parseConfiguration(`  generation code: 0x7
    Element type: Current sensor, subenclosure id: 0, number of possible elements: 1
`)
	page, err := parseThresholdPage(rawThresholds(7, [4]byte{}, [4]byte{0x14, 0x0a, 0x04, 0x02}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	current := page.Limits["0,0"]
	if current.HighCritical.Or(0) != 10 || current.HighWarning.Or(0) != 5 {
		t.Errorf("current high limits %+v, want 10 and 5 %% of nominal", current)
	}
	if current.LowWarning.Present() || current.LowCritical.Present() {
		t.Errorf("reserved low fields were decoded: %+v", current)
	}
}

// The descriptors mean something only against the configuration they were
// written for. Every way the page and the configuration can disagree ends
// in an error and no limits, because a limit on the wrong element is a
// wrong number (ROADMAP 5).
func TestParseThresholdPageRefuses(t *testing.T) {
	t.Parallel()
	cfg := parseConfiguration(configPage)
	all := make([][4]byte, 16)
	for _, tc := range []struct {
		name, out, want string
	}{
		// sg_ses 1.48 and later print only the types that use thresholds;
		// a page that short did not come from this configuration.
		{"only the sensor types", rawThresholds(1, all[:5]...), "carries 5 descriptors and the configuration declares 16"},
		{"one descriptor too many", rawThresholds(1, make([][4]byte, 17)...), "carries 17 descriptors"},
		{"another generation", rawThresholds(2, all...), "generation code 0x2"},
		{"a partial descriptor", strings.TrimSuffix(rawThresholds(1, all...), "\n") + " 00\n", "whole number"},
		{"text instead of hex", "Threshold In diagnostic page:\n  INVOP=0\n", "not a hex byte"},
		{"an error message", "sg_ses: Threshold In dpage not supported\n", "not a hex byte"},
		{"nothing", "", "empty"},
		{"too short", "00 00\n", "shorter than its generation code"},
	} {
		page, err := parseThresholdPage(tc.out, cfg)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one saying %q", tc.name, err, tc.want)
		}
		if len(page.Limits) != 0 {
			t.Errorf("%s: limits decoded from a page that was refused: %v", tc.name, page.Limits)
		}
	}
	// A configuration that does not say how many elements a type has
	// cannot place the descriptors after it.
	uncounted := parseConfiguration("  generation code: 0x1\n    Element type: Temperature sensor, subenclosure id: 0\n")
	if _, err := parseThresholdPage(rawThresholds(1, all[:1]...), uncounted); err == nil ||
		!strings.Contains(err.Error(), "no element count") {
		t.Errorf("a type without a count was decoded: %v", err)
	}
}

// TestParseThresholdPageSixtyBayLayout decodes a page shaped like the
// WD H4060-J's: eleven element types with 60 bays, the enclosure, two
// supplies and eight fans before the 86 temperature sensors, and voltage and
// current sensors after the modules and connectors — 205 descriptors. Each
// limit here is distinct per element, so a decoder that lost its place by
// one descriptor anywhere before a sensor puts the wrong number on it. That
// is what sg_ses 1.48 does in its own text: it skips the four types in front
// of the temperature sensors without stepping over their 75 descriptors.
func TestParseThresholdPageSixtyBayLayout(t *testing.T) {
	t.Parallel()
	types := []struct {
		name  string
		count int
	}{
		{"Array device slot", 60}, {"Enclosure", 1}, {"Power supply", 2}, {"Cooling", 8},
		{"Temperature sensor", 86}, {"Enclosure services controller electronics", 2},
		{"SAS expander", 6}, {"SAS connector", 12}, {"Voltage sensor", 8},
		{"Current sensor", 8}, {"Door", 1},
	}
	var cfg strings.Builder
	cfg.WriteString("  generation code: 0x0\n  type descriptor header and text list\n")
	var descriptors [][4]byte
	for _, ty := range types {
		fmt.Fprintf(&cfg, "    Element type: %s, subenclosure id: 0, number of possible elements: %d\n", ty.name, ty.count)
		for e := -1; e < ty.count; e++ {
			var d [4]byte
			switch ty.name {
			case "Temperature sensor":
				if e >= 0 {
					// high critical 30 + e degrees, high warning 25 + e.
					d = [4]byte{byte(50 + e), byte(45 + e), 20, 0}
				}
			case "Voltage sensor":
				if e >= 0 {
					d = [4]byte{byte(10 + e), byte(6 + e), byte(6 + e), byte(10 + e)}
				}
			case "Current sensor":
				if e >= 0 {
					d = [4]byte{byte(20 + e), byte(10 + e), 0, 0}
				}
			}
			descriptors = append(descriptors, d)
		}
	}
	page, err := parseThresholdPage(rawThresholds(0, descriptors...), parseConfiguration(cfg.String()))
	if err != nil {
		t.Fatal(err)
	}
	for e := 0; e < 86; e++ {
		limits := page.Limits[fmt.Sprintf("4,%d", e)]
		if limits.HighCritical.Or(-1) != float64(30+e) || limits.HighWarning.Or(-1) != float64(25+e) {
			t.Fatalf("temperature sensor %d limits %+v, want %d and %d", e, limits, 30+e, 25+e)
		}
	}
	for e := 0; e < 8; e++ {
		volts := page.Limits[fmt.Sprintf("8,%d", e)]
		if volts.HighCritical.Or(-1) != float64(10+e)/2 || volts.LowCritical.Or(-1) != float64(10+e)/2 {
			t.Errorf("voltage sensor %d limits %+v", e, volts)
		}
		amps := page.Limits[fmt.Sprintf("9,%d", e)]
		if amps.HighCritical.Or(-1) != float64(20+e)/2 || amps.HighWarning.Or(-1) != float64(10+e)/2 {
			t.Errorf("current sensor %d limits %+v", e, amps)
		}
	}
	if n := len(page.Limits); n != 86+8+8 {
		t.Errorf("%d elements got limits, want the 102 sensors", n)
	}
}
