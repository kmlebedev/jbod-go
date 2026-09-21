package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// listFixture is one shelf with one fan, enough for the sections to appear.
func listFixture() *fake {
	return &fake{
		enclosures: []jbod.Enclosure{{Slot: "1:0:0:0", Device: "/dev/sg0"}},
		fans:       []jbod.Fan{{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Speed: jbod.Some(int64(1200))}},
	}
}

// TestListFlagSemantics pins the POSIX behaviour the hand-rolled expansion
// used to approximate: short flags group, long flags take =value, and --
// ends the options (E).
func TestListFlagSemantics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		args          []string
		wantEnclosure bool
		wantFan       bool
		wantErr       string
	}{
		{name: "short", args: []string{"-e"}, wantEnclosure: true},
		{name: "long", args: []string{"--enclosure"}, wantEnclosure: true},
		{name: "grouped", args: []string{"-ed"}, wantEnclosure: true},
		{name: "grouped with fan", args: []string{"-ef"}, wantEnclosure: true, wantFan: true},
		{name: "all grouped", args: []string{"-edf"}, wantEnclosure: true, wantFan: true},
		{name: "separate", args: []string{"-e", "-f"}, wantEnclosure: true, wantFan: true},
		{name: "long with value", args: []string{"--fan=true"}, wantFan: true},
		{name: "short with value", args: []string{"-f=true"}, wantFan: true},
		// An explicitly disabled flag selects nothing, which is an error
		// rather than an empty listing.
		{name: "disabled", args: []string{"-e=false"}, wantErr: "requires"},
		{name: "nothing selected", args: nil, wantErr: "requires"},
		{name: "unknown flag", args: []string{"-z"}, wantErr: "unknown shorthand"},
		{name: "unknown long flag", args: []string{"--zap"}, wantErr: "unknown flag"},
		// The one positional argument list takes is the shelf, so a stray
		// word is read as the name of a shelf and fails as an unknown one.
		// After -- everything is positional, which is how a value that
		// looks like a flag can still be a shelf name.
		{name: "end of options", args: []string{"-e", "--", "-d"}, wantErr: "no enclosure matches"},
		{name: "stray argument", args: []string{"-e", "extra"}, wantErr: "no enclosure matches"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			inv := listFixture()
			var out bytes.Buffer
			err := cmdList(context.Background(), c.args, &out, inv)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("accepted %v: %s", c.args, out.String())
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("%v: error %q, want it to mention %q", c.args, err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v: %v", c.args, err)
			}
			got := collapse(out.String())
			if strings.Contains(got, "SLOT DEVICE VENDOR") != c.wantEnclosure {
				t.Errorf("%v: enclosure section: want present=%v\n%s", c.args, c.wantEnclosure, got)
			}
			if strings.Contains(got, "SLOT IDENT DESCRIPTION") != c.wantFan {
				t.Errorf("%v: fan section: want present=%v\n%s", c.args, c.wantFan, got)
			}
		})
	}
}

// TestListHelp checks that --help is pflag's, not an error the shell should
// see: execute turns pflag.ErrHelp into exit code 0.
func TestListHelp(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := cmdList(context.Background(), []string{"--help"}, &out, &fake{})
	if !errors.Is(err, pflag.ErrHelp) {
		t.Fatalf("--help returned %v", err)
	}
	for _, want := range []string{"--enclosure", "--disks", "--fan", "-e,", "-d,", "-f,"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage is missing %q:\n%s", want, out.String())
		}
	}
}

// TestLEDFlagSemantics covers the repeatable device options and the
// validation that happens before any hardware is touched.
func TestLEDFlagSemantics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "repeated short", args: []string{"-l", "/dev/sda", "-l", "/dev/sdb", "--on"}},
		{name: "long with value", args: []string{"--fault=/dev/sg1", "--off"}},
		{name: "both kinds", args: []string{"-l", "/dev/sda", "-f", "/dev/sg1", "--on"}},
		{name: "no state", args: []string{"-l", "/dev/sda"}, wantErr: "exactly one"},
		{name: "both states", args: []string{"-l", "/dev/sda", "--on", "--off"}, wantErr: "exactly one"},
		{name: "no target", args: []string{"--on"}, wantErr: "requires target"},
		{name: "not a device", args: []string{"-l", "sda", "--on"}, wantErr: "/dev/"},
		{name: "bare slot without a shelf", args: []string{"-l", "5", "--on"}, wantErr: "--enclosure"},
		{name: "bare slot with a shelf", args: []string{"--enclosure", "1:0:0:0", "-l", "5", "--on"}},
		{name: "stray argument", args: []string{"-l", "/dev/sda", "--on", "extra"}, wantErr: "takes no arguments"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			inv := &fake{}
			err := cmdLED(context.Background(), c.args, &bytes.Buffer{}, inv)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("%v: %v", c.args, err)
			case c.wantErr == "":
				if len(inv.leds) == 0 {
					t.Fatalf("%v: no LED was switched", c.args)
				}
			case err == nil:
				t.Fatalf("accepted %v", c.args)
			case !strings.Contains(err.Error(), c.wantErr):
				t.Fatalf("%v: error %q, want it to mention %q", c.args, err, c.wantErr)
			}
		})
	}
}
