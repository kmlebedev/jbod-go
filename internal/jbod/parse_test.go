package jbod

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestField(t *testing.T) {
	t.Parallel()
	out := "  Vendor identification: ACME\nProduct identification:   Shelf  \nRevision level:\n"
	cases := []struct {
		key   string
		want  string
		found bool
	}{
		{"Vendor identification:", "ACME", true},
		{"Product identification:", "Shelf", true},
		{"Revision level:", "", true},
		{"Unit serial number:", "", false},
	}
	for _, c := range cases {
		got, ok := field(out, c.key)
		if got != c.want || ok != c.found {
			t.Errorf("field(%q) = %q, %v; want %q, %v", c.key, got, ok, c.want, c.found)
		}
	}
}

func TestParseLsscsi(t *testing.T) {
	t.Parallel()
	out := strings.Join([]string{
		"[0:0:0:0]  disk    ATA   Boot   1.0  /dev/sda  /dev/sg0",
		"[1:0:0:0]  enclosu ACME  Shelf  1    -         /dev/sg1",
		"[2:0:0:0]  enclosu ACME  Shelf  1    -         -",        // no device
		"[]         enclosu ACME  Shelf  1    -         /dev/sg3", // no slot
		"[../etc]   enclosu ACME  Shelf  1    -         /dev/sg4", // escaping slot
		"[10:0:0:0] enclosure ACME Shelf 1    -         /dev/sg5", // long spelling
		"",
	}, "\n")
	refs, errs := parseLsscsi(out)
	want := []enclosureRef{
		{Slot: "1:0:0:0", Device: "/dev/sg1"},
		{Slot: "10:0:0:0", Device: "/dev/sg5"},
	}
	if len(refs) != len(want) {
		t.Fatalf("got %+v, want %+v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("ref %d: got %+v, want %+v", i, refs[i], want[i])
		}
	}
	// The three unusable lines are reported, not silently dropped.
	if len(errs) != 3 {
		t.Errorf("got %d errors, want 3: %v", len(errs), errs)
	}
	if refs, errs := parseLsscsi(""); refs != nil || errs != nil {
		t.Errorf("empty output: %v %v", refs, errs)
	}
}

func TestParseSgMap(t *testing.T) {
	t.Parallel()
	mapping := parseSgMap("\n/dev/sg0\n/dev/sg1 /dev/sda\n/dev/sg2  /dev/sdb  extra\n")
	if got := mapping["/dev/sg1"]; got != "/dev/sda" {
		t.Errorf("sg1 -> %q", got)
	}
	if got := mapping["/dev/sg2"]; got != "/dev/sdb" {
		t.Errorf("sg2 -> %q", got)
	}
	// A generic device with no block device must not appear at all, so the
	// Map field stays absent rather than becoming an empty string.
	if _, ok := mapping["/dev/sg0"]; ok {
		t.Error("sg0 without a block device was mapped")
	}
}

func TestParseTemperature(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in    string
		want  int64
		found bool
	}{
		{"header\nCurrent temperature: 37 C\n", 37, true},
		{"current Drive Temperature: -2 C", -2, true},
		{"Current temperature: 123 C", 123, true},
		{"Current temperature: not available", 0, false},
		{"Current temperature 37 C", 0, false}, // no colon
		{"Reference temperature: 60 C", 0, false},
		{"", 0, false},
		{"short", 0, false},
		// An unparsable first candidate must not stop a later good line.
		{"Current temperature: 99999999999999999999 C\nCurrent temperature: 40 C", 40, true},
	}
	for _, c := range cases {
		got, ok := parseTemperature(c.in)
		if got != c.want || ok != c.found {
			t.Errorf("parseTemperature(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.found)
		}
	}
}

func TestParseVPD80(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		in    []byte
		want  string
		found bool
	}{
		{"serial", []byte("\x00\x80\x00\x04S123"), "S123", true},
		{"padded", []byte("\x00\x80\x00\x06 S12  "), "S12", true},
		{"trailing junk ignored", []byte("\x00\x80\x00\x04S123junk"), "S123", true},
		{"empty", nil, "", false},
		{"short", []byte("\x00\x80"), "", false},
		{"wrong page", []byte("\x00\x83\x00\x04S123"), "", false},
		{"length past the buffer", []byte("\x00\x80\x00\xffx"), "", false},
		// A run of invalid bytes collapses into one replacement character.
		{"invalid utf-8 replaced", []byte("\x00\x80\x00\x02\xff\xfe"), "�", true},
	}
	for _, c := range cases {
		got, ok := parseVPD80(c.in)
		if got != c.want || ok != c.found {
			t.Errorf("%s: parseVPD80 = %q, %v; want %q, %v", c.name, got, ok, c.want, c.found)
		}
	}
}

func TestParseFanElements(t *testing.T) {
	t.Parallel()
	out := strings.Join([]string{
		"  Element type: Cooling",
		"    Fan A [2,0]  Cooling element",
		"    Fan B [2,1]  Cooling element",
		"    Power supply [1,0]  Power supply element",
		"    Fan C [-1,-2]  Cooling element",
		"    Cooling without an index",
	}, "\n")
	got := parseFanElements(out)
	want := []fanRef{
		{Description: "Fan A", Index: "2,0"},
		{Description: "Fan B", Index: "2,1"},
		{Description: "Fan C", Index: "-1,-2"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("element %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseFanSpeed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		in           string
		speed        int64
		found        bool
		condition    string
		hasCondition bool
	}{
		{
			name: "speed and condition", in: "speed code: 2, Actual speed: 1200 rpm, low speed",
			speed: 1200, found: true, condition: "low speed", hasCondition: true,
		},
		{
			// Two fields only: the enclosure reported a speed and nothing
			// after it, so the condition stays absent instead of "".
			name: "no condition field", in: "Actual speed: 3000 rpm",
			speed: 3000, found: true,
		},
		{
			name: "case and spacing", in: "speed code: 1, actual speed:  900RPM , nominal",
			speed: 900, found: true, condition: "nominal", hasCondition: true,
		},
		{name: "no rpm line", in: "speed code: 0, unknown"},
		{name: "empty", in: ""},
		{
			// Carried over from the original port: the speed comes from the
			// first match in the output and the condition from the last
			// matching line. sg_ses --index= reports a single element, so
			// the two agree in practice.
			name: "several elements", in: "code: 1, Actual speed: 1000 rpm, a\ncode: 2, Actual speed: 2000 rpm, b",
			speed: 1000, found: true, condition: "b", hasCondition: true,
		},
	}
	for _, c := range cases {
		speed, condition, found := parseFanSpeed(c.in)
		if found != c.found {
			t.Errorf("%s: found = %v, want %v", c.name, found, c.found)
			continue
		}
		if speed != c.speed {
			t.Errorf("%s: speed = %d, want %d", c.name, speed, c.speed)
		}
		if condition.Present() != c.hasCondition {
			t.Errorf("%s: condition present = %v, want %v", c.name, condition.Present(), c.hasCondition)
		}
		if got := condition.Or(""); got != c.condition {
			t.Errorf("%s: condition = %q, want %q", c.name, got, c.condition)
		}
	}
}

// The parsers below read untrusted output from the hardware, so they are
// fuzzed: they must never panic and must never claim a value they did not
// find.

func FuzzParseVPD80(f *testing.F) {
	for _, seed := range []string{"", "\x00\x80\x00\x04S123", "\x00\x80\xff\xff", "\x00\x83abcd", "\x00\x80\x00\x02\xff\xfe"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		got, ok := parseVPD80(b)
		if !ok && got != "" {
			t.Fatalf("absent serial carries %q", got)
		}
		// The serial ends up in a metrics label, so it must be valid
		// UTF-8 no matter what the device returned. It can be longer than
		// the input: each run of invalid bytes becomes a replacement
		// character.
		if !utf8.ValidString(got) {
			t.Fatalf("serial %q is not valid UTF-8", got)
		}
	})
}

func FuzzParseTemperature(f *testing.F) {
	for _, seed := range []string{"", "Current temperature: 37 C", "current temperature:-2", "Current temperature: " + strings.Repeat("9", 40)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, ok := parseTemperature(s); !ok && got != 0 {
			t.Fatalf("absent temperature carries %d", got)
		}
	})
}

func FuzzParseFanSpeed(f *testing.F) {
	for _, seed := range []string{"", "Actual speed: 1200 rpm, low", "rpm", "0rpm,,", strings.Repeat("9", 40) + " rpm, x"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		speed, condition, ok := parseFanSpeed(s)
		if !ok && (speed != 0 || condition.Present()) {
			t.Fatalf("absent speed carries %d and %v", speed, condition)
		}
		if speed < 0 {
			t.Fatalf("negative speed %d", speed)
		}
	})
}

func FuzzParseLsscsi(f *testing.F) {
	for _, seed := range []string{"", "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg1", "[..] enclosu x /dev/sg0", "[1:0:0:0] enclosu"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		refs, _ := parseLsscsi(s)
		for _, ref := range refs {
			// Whatever the input, a slot must stay usable as one path
			// element under the sysfs root.
			if ref.Slot == "" || ref.Slot == "." || ref.Slot == ".." || strings.ContainsAny(ref.Slot, "/\\") {
				t.Fatalf("unsafe slot %q", ref.Slot)
			}
			if !strings.HasPrefix(ref.Device, "/dev/") {
				t.Fatalf("device %q is not a /dev/ path", ref.Device)
			}
		}
	})
}
