package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// decode parses the JSON a command printed, so a document that is not valid
// JSON, or that lost a key, fails here rather than in somebody's script.
func decode(t *testing.T, out string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return document
}

func TestHealthJSON(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdHealth(context.Background(), []string{"--json"}, &out, shelf()); err != nil {
		t.Fatal(err)
	}
	document := decode(t, out.String())
	enclosures, ok := document["enclosures"].([]any)
	if !ok || len(enclosures) != 2 {
		t.Fatalf("enclosures = %v", document["enclosures"])
	}
	first, _ := enclosures[0].(map[string]any)
	hardware, _ := first["hardware"].(map[string]any)
	if hardware["level"] != "warning" {
		t.Errorf("hardware level = %v", hardware["level"])
	}
	collection, _ := first["collection"].(map[string]any)
	if collection["complete"] != true {
		t.Errorf("collection.complete = %v", collection["complete"])
	}
	// The three verdicts are separate keys, because a consumer that cannot
	// tell a partial poll from a fault will eventually page somebody for
	// the wrong reason.
	if _, ok := first["summary"]; !ok {
		t.Error("the component roll-up is missing from the document")
	}
	// A shelf that answered nothing reports absent bits, not false ones.
	second, _ := enclosures[1].(map[string]any)
	secondHardware, _ := second["hardware"].(map[string]any)
	if secondHardware["critical"] != nil {
		t.Errorf("an unreadable shelf reported CRIT=%v", secondHardware["critical"])
	}
	if secondHardware["level"] != "unknown" {
		t.Errorf("an unreadable shelf reported level %v", secondHardware["level"])
	}
}

func TestSensorsJSON(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdSensors(context.Background(), []string{"--json"}, &out, shelf()); err != nil {
		t.Fatal(err)
	}
	document := decode(t, out.String())
	enclosures, ok := document["enclosures"].([]any)
	if !ok || len(enclosures) != 2 {
		t.Fatalf("enclosures = %v", document["enclosures"])
	}
	first, _ := enclosures[0].(map[string]any)
	sensors, _ := first["sensors"].([]any)
	if len(sensors) != 4 {
		t.Fatalf("sensors = %d, want 4", len(sensors))
	}
	// Every reading carries its unit, its source and the time it was read,
	// and a value the element did not report is null rather than zero
	// (ROADMAP 3).
	var unreported int
	for _, sensor := range sensors {
		element, _ := sensor.(map[string]any)
		readings, _ := element["readings"].([]any)
		for _, r := range readings {
			reading, _ := r.(map[string]any)
			for _, key := range []string{"kind", "unit", "source", "read_at"} {
				if reading[key] == nil || reading[key] == "" {
					t.Errorf("reading %v has no %s", reading, key)
				}
			}
			if reading["value"] == nil {
				unreported++
			}
		}
	}
	if unreported != 1 {
		t.Errorf("readings without a value = %d, want 1", unreported)
	}
	// A shelf with no sensors renders as an empty list, not as null: the
	// difference between "asked and there are none" and "not asked".
	second, _ := enclosures[1].(map[string]any)
	if sensors, ok := second["sensors"].([]any); !ok || len(sensors) != 0 {
		t.Errorf("second shelf sensors = %v", second["sensors"])
	}
}

func TestComponentsJSON(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdList(context.Background(), []string{"--components", "--json"}, &out, shelf()); err != nil {
		t.Fatal(err)
	}
	document := decode(t, out.String())
	components, ok := document["components"].([]any)
	if !ok || len(components) != 8 {
		t.Fatalf("components = %v", document["components"])
	}
	// The sections nobody asked for stay out of the document.
	for _, key := range []string{"slots", "disks", "fans", "enclosures"} {
		if _, present := document[key]; present {
			t.Errorf("%s was rendered without being asked for", key)
		}
	}
	first, _ := components[0].(map[string]any)
	if first["sas_addresses"] == nil {
		t.Errorf("the bay lost its SAS address: %v", first)
	}
	if first["device"] != "/dev/sg1" {
		t.Errorf("bay device = %v", first["device"])
	}
}

// Each report takes a shelf the same way, and naming one twice is an error
// rather than a silent winner.
func TestInspectCommandsSelectOneShelf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"positional", []string{"0x5000ccab05629d00"}, "1:0:0:0"},
		{"flag", []string{"--enclosure-id", "0x5000ccab05629d00"}, "1:0:0:0"},
		{"alias", []string{"--enclosure", "0x5000ccab05629d00"}, "1:0:0:0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := cmdHealth(context.Background(), c.args, &out, shelf()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), c.want) {
				t.Errorf("output does not name %s:\n%s", c.want, out.String())
			}
			if strings.Contains(out.String(), "10:0:0:0") {
				t.Errorf("the other shelf was reported anyway:\n%s", out.String())
			}
		})
	}
}

func TestInspectCommandErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"twice", []string{"--enclosure-id", "a", "b"}, "named twice"},
		{"too many", []string{"a", "b"}, "at most one enclosure"},
		{"unknown shelf", []string{"nosuchshelf"}, "no enclosure matches"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			err := cmdHealth(context.Background(), c.args, &out, shelf())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want one mentioning %q", err, c.want)
			}
		})
	}
}

// A missing tool is reported once, before anything talks to the hardware.
func TestInspectCommandsPreflight(t *testing.T) {
	t.Parallel()
	for name, command := range map[string]func(context.Context, []string, *bytes.Buffer) error{
		"health": func(ctx context.Context, args []string, out *bytes.Buffer) error {
			return cmdHealth(ctx, args, out, &fake{preflight: errors.New("missing tools: sg_ses")})
		},
		"sensors": func(ctx context.Context, args []string, out *bytes.Buffer) error {
			return cmdSensors(ctx, args, out, &fake{preflight: errors.New("missing tools: sg_ses")})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			err := command(context.Background(), nil, &out)
			if err == nil || !strings.Contains(err.Error(), "missing tools") {
				t.Errorf("error = %v", err)
			}
			if out.Len() != 0 {
				t.Errorf("a failed preflight printed a report:\n%s", out.String())
			}
		})
	}
}

// list keeps its sections independent: --components must not swallow the
// others, and the others must not run the inspection.
func TestListComponentsSection(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := cmdList(context.Background(), []string{"-e", "-c"}, &out, shelf()); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if !strings.Contains(printed, "VENDOR") {
		t.Errorf("the enclosure section is missing:\n%s", printed)
	}
	if !strings.Contains(printed, "SAS ADDRESS") {
		t.Errorf("the component section is missing:\n%s", printed)
	}
	var fans bytes.Buffer
	if err := cmdList(context.Background(), []string{"-f"}, &fans, shelf()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fans.String(), "SAS ADDRESS") {
		t.Errorf("--fan printed the component section:\n%s", fans.String())
	}
}
