package jbod

import (
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

const thresholdPage = `Threshold In diagnostic page:
  generation code: 0x1
  Element type: Temperature sensor, subenclosure id: 0 [ti=3]
    Overall threshold: high critical=100 C, high warning=95 C, low warning=0 C, low critical=-19 C
    Element 0 threshold: high critical=65 C, high warning=60 C, low warning=0 C, low critical=-19 C
    Element 1 threshold: high critical=65 C, high warning=60 C, low warning=0 C, low critical=-19 C
  Element type: Voltage sensor, subenclosure id: 1 [ti=4]
    Element 0 threshold: high critical=13.20 V, high warning=12.96 V, low warning=11.04 V, low critical=10.80 V
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

func TestParseThresholds(t *testing.T) {
	t.Parallel()
	thresholds := parseThresholds(thresholdPage, typeIndices(parseJoinElements(joinPage)))
	sensor, ok := thresholds["3,0"]
	if !ok {
		t.Fatalf("no thresholds for the first temperature sensor: %v", thresholds)
	}
	if v, ok := sensor.HighCritical.Get(); !ok || v != 65 {
		t.Errorf("high critical = %v, want 65", sensor.HighCritical)
	}
	if v, ok := sensor.LowCritical.Get(); !ok || v != -19 {
		t.Errorf("low critical = %v, want -19", sensor.LowCritical)
	}
	// The overall threshold of a type is not a threshold of any element of
	// it, so it is kept apart and never attached to one.
	overall, ok := thresholds["3,-1"]
	if !ok {
		t.Fatal("the overall threshold was dropped")
	}
	if v, ok := overall.HighCritical.Get(); !ok || v != 100 {
		t.Errorf("overall high critical = %v, want 100", overall.HighCritical)
	}
	volts, ok := thresholds["4,0"]
	if !ok {
		t.Fatalf("no thresholds for the voltage sensor: %v", thresholds)
	}
	if v, ok := volts.HighWarning.Get(); !ok || v != 12.96 {
		t.Errorf("voltage high warning = %v, want 12.96", volts.HighWarning)
	}
}

// Without the "[ti=N]" hint the page has to be joined by type, in the order
// the types appear, because the header carries no type index of its own.
func TestParseThresholdsWithoutTypeIndex(t *testing.T) {
	t.Parallel()
	page := strings.ReplaceAll(thresholdPage, " [ti=3]", "")
	page = strings.ReplaceAll(page, " [ti=4]", "")
	thresholds := parseThresholds(page, typeIndices(parseJoinElements(joinPage)))
	if _, ok := thresholds["3,0"]; !ok {
		t.Errorf("temperature thresholds were not joined: %v", thresholds)
	}
	if _, ok := thresholds["4,0"]; !ok {
		t.Errorf("voltage thresholds were not joined: %v", thresholds)
	}
}

// A shelf that does not implement the page reports an error, and an error
// is not a threshold of zero.
func TestParseThresholdsUnsupported(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "sg_ses: Threshold In dpage not supported\n"} {
		if got := parseThresholds(in, nil); len(got) != 0 {
			t.Errorf("%q produced %v", in, got)
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

// The same leak in the threshold page would write one element's limits onto
// another element's reading, which is a wrong number rather than an absent
// one.
func TestParseThresholdsUnknownType(t *testing.T) {
	t.Parallel()
	const page = `Threshold In diagnostic page:
  generation code: 0x1
  Element type: Temperature sensor, subenclosure id: 0 [ti=2]
    Element 0 threshold: high critical=65 C, high warning=60 C, low warning=0 C, low critical=-19 C
  Element type: vendor specific [0x81], subenclosure id: 0 [ti=3]
    Element 0 threshold: high critical=99, high warning=98, low warning=2, low critical=1
`
	thresholds := parseThresholds(page, nil)
	sensor, ok := thresholds["2,0"]
	if !ok {
		t.Fatalf("the sensor lost its thresholds: %v", thresholds)
	}
	if v, present := sensor.HighCritical.Get(); !present || v != 65 {
		t.Errorf("high critical = %v, want 65 (the vendor element's limits overwrote it)", sensor.HighCritical)
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
