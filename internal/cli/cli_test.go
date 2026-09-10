package cli

import (
	"bytes"
	"context"
	"fmt"
	"github.com/kmlebedev/jbod-go/internal/jbod"
	"io"
	"strings"
	"testing"
)

func TestValidationAndHelp(t *testing.T) {
	c := jbod.New(jbod.WithRunner(func(context.Context, string, ...string) (string, error) {
		t.Error("unexpected hardware access")
		return "", nil
	}))
	for _, args := range [][]string{{"help"}, {"--version"}, {"list", "--help"}} {
		var out bytes.Buffer
		err := Run(context.Background(), args, &out, io.Discard, c)
		if out.Len() == 0 {
			t.Fatalf("%v %v", args, err)
		}
	}
	for _, args := range [][]string{{"bad"}, {"list"}, {"list", "-z"}, {"led", "-l", "/dev/sda"}, {"led", "-l", "/dev/sda", "--on", "--off"}, {"led", "--on"}, {"prometheus", "--port", "bad"}, {"prometheus", "--ip", "bad"}, {"led", "-l", "NONE", "--on"}} {
		if Run(context.Background(), args, &bytes.Buffer{}, io.Discard, c) == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestList(t *testing.T) {
	root := t.TempDir()
	runner := func(_ context.Context, name string, _ ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[0:0:0:0] enclosu ACME Shelf 1 - /dev/sg0", nil
		case "sg_inq":
			return "Vendor identification: ACME\nProduct identification: Shelf", nil
		default:
			return "", fmt.Errorf("unexpected %s", name)
		}
	}
	c := jbod.New(jbod.WithSysfs(root), jbod.WithRunner(runner))
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"list", "-e"}, &out, io.Discard, c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Shelf") || !strings.Contains(out.String(), "/dev/sg0") {
		t.Fatal(out.String())
	}
	// Empty discovery accepts clap-style combined options without querying disks.
	empty := jbod.New(jbod.WithSysfs(root), jbod.WithRunner(func(context.Context, string, ...string) (string, error) { return "", nil }))
	if err := Run(context.Background(), []string{"list", "-ed"}, &out, io.Discard, empty); err != nil {
		t.Fatal(err)
	}
}
