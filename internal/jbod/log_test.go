package jbod

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// logged builds a client over a one-slot shelf whose sensors fail, together
// with the buffer its logger writes to. A single slog handler serialises its
// own writes, so the buffer is safe for the parallel collection.
func logged(t *testing.T, level slog.Level) (*Client, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "1:0:0:0", "Slot 01, front")
	if err := os.MkdirAll(filepath.Join(base, "device", "scsi_generic", "sg1"), 0o755); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
	client := New(WithSysfs(root), WithLogger(logger), WithRunner(func(_ context.Context, name string, args ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n", nil
		case "sg_inq":
			return "Vendor identification: ACME\n", nil
		case "sg_map":
			return "", nil
		case "scsi_temperature":
			return "", fmt.Errorf("scsi_temperature: no such device")
		case "sginfo":
			return "", fmt.Errorf("sginfo: timed out")
		case "sg_ses":
			if args[0] == "-j" {
				return "Fan A [2,0] Cooling\n", nil
			}
			return "speed code: 0, unreadable\n", nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	}))
	return client, buf
}

// TestFailedCommandsAreLogged is the F contract for the collector: a daemon
// that walks the hardware every 15 seconds used to write nothing at all, so
// a degrading shelf left no trace beyond a counter.
func TestFailedCommandsAreLogged(t *testing.T) {
	t.Parallel()
	client, buf := logged(t, slog.LevelDebug)
	s, err := client.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		`"level":"WARN"`,
		`"msg":"collection error"`,
		`"collector":"disks"`,
		`"collector":"fans"`,
		"scsi_temperature",
		"sginfo",
		"no fan RPM",
		`"msg":"collection finished"`,
		`"duration"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in the log:\n%s", want, got)
		}
	}
	// One line per counted failure, no more: two dead sensors on the disk
	// and one unreadable fan.
	failures := 0
	for _, n := range s.Errors {
		failures += n
	}
	if failures != 3 {
		t.Fatalf("counted %d failures, want 3: %v", failures, s.Errors)
	}
	if n := strings.Count(got, `"msg":"collection error"`); n != failures {
		t.Errorf("%d log lines for %d failures", n, failures)
	}
	// A pass that lost something is worth more than debug level.
	if !strings.Contains(got, `"level":"INFO","msg":"collection finished"`) {
		t.Errorf("a lossy pass was not reported at info level:\n%s", got)
	}
}

// TestQuietByDefault keeps the library silent unless a logger is given: the
// CLI must not grow diagnostics on stderr on its own.
func TestQuietByDefault(t *testing.T) {
	t.Parallel()
	client := New(WithSysfs(t.TempDir()), WithRunner(func(context.Context, string, ...string) (string, error) {
		return "", fmt.Errorf("nothing here")
	}))
	if _, err := client.Enclosures(context.Background()); err == nil {
		t.Fatal("expected the failing command to be reported as an error")
	}
	// Nothing to assert on a discard handler beyond it not panicking; the
	// point is that New needs no logger to be usable.
	client, buf := logged(t, slog.LevelError)
	if _, err := client.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("records below the level were emitted:\n%s", got)
	}
}
