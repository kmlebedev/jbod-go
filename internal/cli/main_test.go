package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// TestExecute covers what used to be duplicated in both main packages (D4):
// the exit code and where the message goes.
func TestExecute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		code   int
		stderr string
	}{
		{name: "success", code: 0},
		// pflag already printed its usage to stdout, so a help request is
		// not an error for the shell.
		{name: "help", err: pflag.ErrHelp, code: 0},
		{name: "wrapped help", err: fmt.Errorf("parse: %w", pflag.ErrHelp), code: 0},
		{name: "failure", err: errors.New("no enclosures"), code: 1, stderr: "jbod: no enclosures"},
	}
	for _, c := range cases {
		var out, errOut bytes.Buffer
		command := func(context.Context, []string, io.Writer, io.Writer, *jbod.Client) error { return c.err }
		code := execute(context.Background(), "jbod", command, nil, &out, &errOut, jbod.New())
		if code != c.code {
			t.Errorf("%s: exit code %d, want %d", c.name, code, c.code)
		}
		if got := strings.TrimSpace(errOut.String()); got != c.stderr {
			t.Errorf("%s: stderr %q, want %q", c.name, got, c.stderr)
		}
	}
}

// TestExecutePassesTheArguments makes sure the shared entry point hands the
// command what it was given, for both binaries.
func TestExecutePassesTheArguments(t *testing.T) {
	t.Parallel()
	var seen []string
	command := func(_ context.Context, args []string, out, errOut io.Writer, c *jbod.Client) error {
		seen = args
		if c == nil {
			t.Error("no client")
		}
		fmt.Fprint(out, "done")
		return nil
	}
	var out, errOut bytes.Buffer
	args := []string{"list", "-ef"}
	if code := execute(context.Background(), "exporter", command, args, &out, &errOut, jbod.New()); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if strings.Join(seen, " ") != "list -ef" {
		t.Errorf("arguments %v", seen)
	}
	if out.String() != "done" {
		t.Errorf("stdout %q", out.String())
	}
}

// TestMainWiring covers what only the process entry point does: the
// arguments it takes from the command line, the context it builds for the
// signals and the client it hands over.
func TestMainWiring(t *testing.T) {
	// No t.Parallel: os.Args is process-wide.
	saved := os.Args
	t.Cleanup(func() { os.Args = saved })
	os.Args = []string{"jbod", "list", "-ef"}
	var seen []string
	code := Main("jbod", func(ctx context.Context, args []string, out, errOut io.Writer, c *jbod.Client) error {
		seen = args
		if ctx == nil {
			t.Error("no context")
		}
		if c == nil {
			t.Error("no client")
		}
		if out == nil || errOut == nil {
			t.Error("no streams")
		}
		return nil
	})
	if code != 0 {
		t.Errorf("exit code %d", code)
	}
	if strings.Join(seen, " ") != "list -ef" {
		t.Errorf("arguments %v", seen)
	}
	// A failing command exits non-zero; the message goes to the process
	// stderr, which TestBinary in cmd/jbod checks end to end.
	code = Main("jbod", func(context.Context, []string, io.Writer, io.Writer, *jbod.Client) error {
		return errors.New("no enclosures")
	})
	if code != 1 {
		t.Errorf("exit code %d for a failing command", code)
	}
}
