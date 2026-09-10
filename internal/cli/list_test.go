package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jbod-go/internal/jbod"
)

const (
	enclosureHeader = "SLOT     DEVICE"
	fanHeader       = "SLOT     IDENT"
)

func listClient(t *testing.T) *jbod.Client {
	t.Helper()
	root := t.TempDir()
	// An enclosure directory with no slots keeps Disks cheap but valid.
	if err := os.MkdirAll(filepath.Join(root, "1:0:0:0"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &jbod.Client{Sysfs: root, Run: func(_ context.Context, name string, args ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n", nil
		case "sg_inq":
			return "Vendor identification: ACME\nProduct identification: Shelf\n", nil
		case "sg_map":
			return "", nil
		case "sg_ses":
			if len(args) > 0 && args[0] == "-j" {
				return "Fan A [2,0] Cooling\n", nil
			}
			return "speed code: 2, Actual speed: 1200 rpm, low speed\n", nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	}}
}

// Every flag selects its own section; combining them must not drop one.
func TestListSectionsAreIndependent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		args          []string
		wantEnclosure bool
		wantFan       bool
	}{
		{"enclosure", []string{"list", "-e"}, true, false},
		{"disks", []string{"list", "-d"}, true, false},
		{"fan", []string{"list", "-f"}, false, true},
		{"enclosure+fan", []string{"list", "-e", "-f"}, true, true},
		{"disks+fan", []string{"list", "-d", "-f"}, true, true},
		{"all", []string{"list", "-e", "-d", "-f"}, true, true},
		{"combined short", []string{"list", "-ef"}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := Run(context.Background(), c.args, &out, listClient(t)); err != nil {
				t.Fatalf("%v: %v", c.args, err)
			}
			got := out.String()
			if strings.Contains(got, enclosureHeader) != c.wantEnclosure {
				t.Errorf("%v: enclosure section present=%v, want %v\n%s", c.args, !c.wantEnclosure, c.wantEnclosure, got)
			}
			if strings.Contains(got, fanHeader) != c.wantFan {
				t.Errorf("%v: fan section present=%v, want %v\n%s", c.args, !c.wantFan, c.wantFan, got)
			}
			if c.wantFan && !strings.Contains(got, "Fan A") {
				t.Errorf("%v: missing fan row\n%s", c.args, got)
			}
		})
	}
}

// The standalone exporter binary never reaches Run, so it needs its own
// --help and --version.
func TestExporterHelpAndVersion(t *testing.T) {
	t.Parallel()
	c := &jbod.Client{Run: func(context.Context, string, ...string) (string, error) {
		t.Fatal("unexpected hardware access")
		return "", nil
	}}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--help"}, "prometheus-jbod-exporter"},
		{[]string{"-h"}, "prometheus-jbod-exporter"},
		{[]string{"help"}, "prometheus-jbod-exporter"},
		{[]string{"--version"}, "jbod-go " + Version},
		{[]string{"-V"}, "jbod-go " + Version},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		if err := Exporter(context.Background(), tc.args, &out, c); err != nil {
			t.Fatalf("exporter %v: %v", tc.args, err)
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Fatalf("exporter %v: got %q, want %q", tc.args, out.String(), tc.want)
		}
	}
	// Reachable through the subcommand too, and both report one version.
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"prometheus", "--version"}, &out, c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "jbod-go "+Version) {
		t.Fatalf("jbod prometheus --version: %q", out.String())
	}
	out.Reset()
	if err := Run(context.Background(), []string{"--version"}, &out, c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "jbod-go "+Version) {
		t.Fatalf("jbod --version: %q", out.String())
	}
}
