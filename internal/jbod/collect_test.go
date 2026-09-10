package jbod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// gauge records how many external commands ran at the same time.
type gauge struct {
	mu       sync.Mutex
	current  int
	max      int
	released chan struct{}
	limit    int
	wait     time.Duration
	once     sync.Once
}

func newGauge(limit int, wait time.Duration) *gauge {
	return &gauge{released: make(chan struct{}), limit: limit, wait: wait}
}

// hold blocks until limit commands are in flight at once, so a sequential
// implementation cannot satisfy it and a parallel one does not depend on
// timing. The wait is bounded so a failure reports instead of hanging.
func (g *gauge) hold() {
	g.mu.Lock()
	g.current++
	if g.current > g.max {
		g.max = g.current
	}
	reached := g.current >= g.limit
	g.mu.Unlock()
	if reached {
		g.once.Do(func() { close(g.released) })
	}
	select {
	case <-g.released:
	case <-time.After(g.wait):
	}
	g.mu.Lock()
	g.current--
	g.mu.Unlock()
}

func (g *gauge) peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.max
}

// shelf builds a sysfs tree with n slots in one enclosure.
func shelf(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := 1; i <= n; i++ {
		dir := filepath.Join(root, "1:0:0:0", fmt.Sprintf("Slot %02d, front", i), "device", "scsi_generic", fmt.Sprintf("sg%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestTelemetryRunsInParallel is the B1 regression: two commands per disk run
// strictly one after another before the worker pool.
func TestTelemetryRunsInParallel(t *testing.T) {
	t.Parallel()
	const limit = 8
	g := newGauge(limit, 2*time.Second)
	c := New(WithSysfs(shelf(t, 16)), WithConcurrency(limit), WithRunner(func(_ context.Context, name string, _ ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n", nil
		case "sg_inq":
			return "Vendor identification: ACME\n", nil
		case "sg_map":
			return "", nil
		case "scsi_temperature":
			g.hold()
			return "Current temperature: 30 C\n", nil
		case "sginfo":
			g.hold()
			return "Revision level: FW1\n", nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	}))
	ctx := context.Background()
	es, err := c.Enclosures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := c.Disks(ctx, es, DiskOptions{WithTelemetry: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 16 {
		t.Fatalf("got %d disks, want 16", len(ds))
	}
	if peak := g.peak(); peak < 2 {
		t.Fatalf("telemetry peak concurrency %d: commands ran sequentially", peak)
	} else if peak > limit {
		t.Fatalf("telemetry peak concurrency %d exceeds limit %d", peak, limit)
	}
	// Order must not depend on which goroutine finished first.
	for i := range ds {
		if want := fmt.Sprintf("Slot %02d", i+1); ds[i].Slot != want {
			t.Fatalf("position %d: got %q, want %q", i, ds[i].Slot, want)
		}
	}
}

// TestConcurrencyLimitIsHonoured pins the semaphore: an expander must not see
// more than the configured number of requests at once.
func TestConcurrencyLimitIsHonoured(t *testing.T) {
	t.Parallel()
	const limit = 3
	// The gauge waits for one more command than allowed: that barrier is
	// never reached, so every command overlaps with its neighbours for the
	// whole hold and a leaked limit would show up as a higher peak.
	g := newGauge(limit+1, 20*time.Millisecond)
	c := New(WithSysfs(shelf(t, 12)), WithConcurrency(limit), WithRunner(func(_ context.Context, name string, _ ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n", nil
		case "sg_inq", "sg_map":
			return "", nil
		}
		g.hold()
		return "Current temperature: 30 C\nRevision level: FW1\n", nil
	}))
	es, err := c.Enclosures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Disks(context.Background(), es, DiskOptions{WithTelemetry: true}); err != nil {
		t.Fatal(err)
	}
	if peak := g.peak(); peak > limit {
		t.Fatalf("peak concurrency %d exceeds limit %d", peak, limit)
	}
}

// TestCommandTimeout checks that the per-command budget is applied to the
// injected runner too, not only to the exec.Cmd inside run.
func TestCommandTimeout(t *testing.T) {
	t.Parallel()
	c := New(WithSysfs(t.TempDir()), WithCommandTimeout(20*time.Millisecond), WithRunner(func(ctx context.Context, _ string, _ ...string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}))
	start := time.Now()
	if _, err := c.Enclosures(context.Background()); err == nil {
		t.Fatal("expected the command timeout to fail lsscsi")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("command ran for %s, timeout not applied", elapsed)
	}
}

// partial builds a client with two enclosures where the second has no sysfs
// tree and one fan reports no RPM.
func partial(t *testing.T) *Client {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "1:0:0:0", "Slot 01, front")
	if err := os.MkdirAll(filepath.Join(base, "device", "scsi_generic", "sg1"), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(WithSysfs(root), WithRunner(func(_ context.Context, name string, args ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n[9:0:0:0] enclosu ACME Shelf 1 - /dev/sg9\n", nil
		case "sg_inq":
			return "Vendor identification: ACME\nUnit serial number: ENC" + args[0] + "\n", nil
		case "sg_map":
			return "/dev/sg1 /dev/sda\n", nil
		case "scsi_temperature":
			return "Current temperature: 37 C\n", nil
		case "sginfo":
			return "Revision level: FW1\n", nil
		case "sg_ses":
			if args[0] == "-j" {
				return "Fan A [2,0] Cooling\nFan B [2,1] Cooling\n", nil
			}
			if strings.HasSuffix(args[0], "2,1") {
				// A sensor that answers without an RPM line used to fail
				// the whole scrape (A6).
				return "speed code: 0, unknown\n", nil
			}
			return "speed code: 2, Actual speed: 1200 rpm, low speed\n", nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	}))
}

func TestPartialCollectionKeepsData(t *testing.T) {
	t.Parallel()
	c := partial(t)
	ctx := context.Background()
	es, err := c.Enclosures(ctx)
	if err != nil || len(es) != 2 {
		t.Fatalf("%v %v", es, err)
	}
	// The strict API still reports the unreadable shelf, but returns the
	// disks it did find.
	ds, err := c.Disks(ctx, es, DiskOptions{WithTelemetry: true})
	if err == nil {
		t.Fatal("expected an error for the enclosure without a sysfs tree")
	}
	if len(ds) != 1 || ds[0].Temperature.Or(0) != 37 {
		t.Fatalf("lost the readable disk: %+v", ds)
	}
	// A fan without an RPM reading is skipped, not fatal.
	fs, err := c.Fans(ctx, es)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("got %d fans, want the two readable ones: %+v", len(fs), fs)
	}
	for _, f := range fs {
		if f.Description != "Fan A" || f.Speed != 1200 {
			t.Fatalf("unexpected fan %+v", f)
		}
	}
	// Collect never fails on partial data and counts what went wrong.
	s, err := c.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Up || s.Duration <= 0 {
		t.Fatalf("up=%v duration=%s", s.Up, s.Duration)
	}
	if s.Errors[CollectorDisks] == 0 || s.Errors[CollectorFans] == 0 {
		t.Fatalf("errors not counted: %v", s.Errors)
	}
	if len(s.Disks) != 1 || len(s.Fans) != 2 {
		t.Fatalf("snapshot lost data: %d disks, %d fans", len(s.Disks), len(s.Fans))
	}
}

// TestScrapeCancellationStopsCommands makes sure a cancelled scrape context
// reaches the external commands instead of letting them run to completion.
func TestScrapeCancellationStopsCommands(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	var once sync.Once
	c := New(WithSysfs(t.TempDir()), WithRunner(func(ctx context.Context, _ string, _ ...string) (string, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return "", ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Collect(ctx)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled collection reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled collection did not return")
	}
}
