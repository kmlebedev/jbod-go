package exporter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// collector is a Collector that answers from fixed data, so the exporter's
// own behaviour (routing, deduplication, caching, counters) is tested without
// a shelf or a sysfs tree.
type collector struct {
	mu       sync.Mutex
	snapshot jbod.Snapshot
	err      error
	calls    int
	// block, when set, holds every pass until it is closed.
	block chan struct{}
	// started is closed once the first pass reached the hardware.
	started chan struct{}
	once    sync.Once
}

func (c *collector) Collect(ctx context.Context) (jbod.Snapshot, error) {
	c.mu.Lock()
	c.calls++
	block, started := c.block, c.started
	snapshot, err := c.snapshot, c.err
	c.mu.Unlock()
	if started != nil {
		c.once.Do(func() { close(started) })
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return jbod.Snapshot{}, ctx.Err()
		}
	}
	return snapshot, err
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func partialSnapshot() jbod.Snapshot {
	return jbod.Snapshot{
		Enclosures: []jbod.Enclosure{{Slot: "1:0:0:0", Device: "/dev/sg0"}},
		Disks:      []jbod.Disk{{Enclosure: "1:0:0:0", Slot: "Slot 01", Temperature: jbod.Some(int64(37))}},
		Fans:       []jbod.Fan{{Slot: "1:0:0:0", Description: "Fan A", Index: "2,0", Speed: jbod.Some(int64(1200))}},
		Errors:     map[string]int{jbod.CollectorFans: 1},
		Duration:   12 * time.Millisecond,
		Up:         true,
	}
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(method, path, nil))
	return r
}

func TestHandlerRoutes(t *testing.T) {
	t.Parallel()
	e := New(&collector{snapshot: partialSnapshot()})
	h := e.Handler()

	r := get(t, h, "GET", "/metrics")
	if r.Code != http.StatusOK || !strings.Contains(r.Header().Get("Content-Type"), "version=0.0.4") {
		t.Fatalf("%d %q", r.Code, r.Header().Get("Content-Type"))
	}
	if !strings.Contains(r.Body.String(), "number_of_enclosures 1") {
		t.Fatalf("body:\n%s", r.Body.String())
	}
	// HEAD answers with the headers and no body, so a probe does not have to
	// read a full scrape.
	if r := get(t, h, "HEAD", "/metrics"); r.Code != http.StatusOK || r.Body.Len() != 0 {
		t.Fatalf("HEAD: %d, %d bytes", r.Code, r.Body.Len())
	}
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"GET", "/", http.StatusOK},
		{"GET", "/missing", http.StatusNotFound},
		{"POST", "/metrics", http.StatusMethodNotAllowed},
		{"DELETE", "/", http.StatusMethodNotAllowed},
	} {
		if r := get(t, h, tc.method, tc.path); r.Code != tc.code {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, r.Code, tc.code)
		}
	}
}

// TestTotalFailureIs503 keeps the one case that is not partial success: the
// enclosure listing itself did not work.
func TestTotalFailureIs503(t *testing.T) {
	t.Parallel()
	c := &collector{err: errors.New(`lsscsi: executable file not found in $PATH`)}
	h := New(c).Handler()
	r := get(t, h, "GET", "/metrics")
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d: %s", r.Code, r.Body.String())
	}
	if !strings.Contains(r.Body.String(), "lsscsi") {
		t.Errorf("the error is not reported: %s", r.Body.String())
	}
	// A failed pass is not cached: the next scrape retries the hardware.
	e := New(c, WithCacheTTL(time.Minute))
	fh := e.Handler()
	get(t, fh, "GET", "/metrics")
	get(t, fh, "GET", "/metrics")
	if e.Passes() != 2 {
		t.Errorf("%d passes, want a retry after a failure", e.Passes())
	}
}

// TestHealthMetrics is the B5 contract: a partial collection is a 200 with
// health metrics, and the error series is a counter.
func TestHealthMetrics(t *testing.T) {
	t.Parallel()
	e := New(&collector{snapshot: partialSnapshot()})
	h := e.Handler()
	body := get(t, h, "GET", "/metrics").Body.String()
	for _, want := range []string{
		"jbod_up 1",
		"jbod_scrape_duration_seconds ",
		`jbod_scrape_errors_total{collector="enclosures"} 0`,
		`jbod_scrape_errors_total{collector="fans"} 1`,
		`jbod_slot_temperature{enclosure="1:0:0:0",slot="Slot 01"} 37`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}
	body = get(t, h, "GET", "/metrics").Body.String()
	if !strings.Contains(body, `jbod_scrape_errors_total{collector="fans"} 2`) {
		t.Fatalf("counter did not accumulate:\n%s", body)
	}
}

// TestConcurrentScrapesShareOnePass is the B2 regression: Prometheus and a
// manual curl used to walk the shelf twice, spawning a hundred processes each.
func TestConcurrentScrapesShareOnePass(t *testing.T) {
	t.Parallel()
	c := &collector{snapshot: partialSnapshot(), block: make(chan struct{}), started: make(chan struct{})}
	e := New(c)
	h := e.Handler()
	const requests = 4
	bodies := make([]string, requests)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bodies[i] = get(t, h, "GET", "/metrics").Body.String()
		}(i)
	}
	select {
	case <-c.started:
	case <-time.After(5 * time.Second):
		t.Fatal("no pass reached the collector")
	}
	// Hold the pass long enough for the other requests to arrive. Without
	// the deduplication they would all be inside Collect by now, which is
	// what the assertions below catch.
	deadline := time.Now().Add(200 * time.Millisecond)
	for c.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(c.block)
	wg.Wait()
	if passes := e.Passes(); passes != 1 {
		t.Fatalf("%d collection passes for %d overlapping requests, want 1", passes, requests)
	}
	if c.count() != 1 {
		t.Fatalf("collector called %d times", c.count())
	}
	for i, body := range bodies {
		if !strings.Contains(body, "number_of_enclosures 1") {
			t.Fatalf("request %d got %q", i, body)
		}
	}
}

// collected keeps only the series that come from the snapshot. The process,
// Go runtime and handler metrics are gathered live on every request and are
// deliberately never cached, so only the snapshot part is expected to be
// identical between a collection and a cache hit.
func collected(body string) string {
	var b strings.Builder
	for line := range strings.Lines(body) {
		if strings.Contains(line, "jbod_") || strings.Contains(line, "number_of_enclosures") {
			b.WriteString(line)
		}
	}
	return b.String()
}

// TestCacheTTLServesRecentResult covers the other half of B2: a scrape that
// arrives right after another one is answered without touching the hardware.
func TestCacheTTLServesRecentResult(t *testing.T) {
	t.Parallel()
	c := &collector{snapshot: partialSnapshot()}
	e := New(c, WithCacheTTL(time.Minute))
	h := e.Handler()
	first := get(t, h, "GET", "/metrics").Body.String()
	second := get(t, h, "GET", "/metrics").Body.String()
	if e.Passes() != 1 || c.count() != 1 {
		t.Fatalf("%d passes and %d collector calls, want 1 within the cache TTL", e.Passes(), c.count())
	}
	if collected(first) != collected(second) {
		t.Fatalf("cached response differs from the collected one.\n--- first ---\n%s\n--- second ---\n%s",
			collected(first), collected(second))
	}
	// The process metrics are gathered live on purpose, so they are outside
	// that comparison but must still be in both responses on a host that has
	// them (A9). They now come from the client library's process collector,
	// which reads /proc and therefore only publishes on Linux.
	if runtime.GOOS == "linux" {
		for i, body := range []string{first, second} {
			if !strings.Contains(body, "\nprocess_cpu_seconds_total ") {
				t.Errorf("response %d carries no process metrics:\n%s", i, body)
			}
		}
	}
	// A zero TTL keeps the previous behaviour: every scrape is fresh.
	fresh := New(c)
	fh := fresh.Handler()
	get(t, fh, "GET", "/metrics")
	get(t, fh, "GET", "/metrics")
	if fresh.Passes() != 2 {
		t.Fatalf("%d collection passes with caching disabled, want 2", fresh.Passes())
	}
}

// TestScrapeTimeoutBoundsThePass checks the budget the handler puts on the
// collection.
func TestScrapeTimeoutBoundsThePass(t *testing.T) {
	t.Parallel()
	deadlines := make(chan time.Duration, 1)
	e := New(deadlineCollector{deadlines}, WithScrapeTimeout(250*time.Millisecond))
	if r := get(t, e.Handler(), "GET", "/metrics"); r.Code != http.StatusOK {
		t.Fatalf("%d: %s", r.Code, r.Body.String())
	}
	budget := <-deadlines
	if budget <= 0 || budget > 250*time.Millisecond {
		t.Fatalf("collection budget %s, want at most 250ms", budget)
	}
}

type deadlineCollector struct {
	deadlines chan time.Duration
}

func (d deadlineCollector) Collect(ctx context.Context) (jbod.Snapshot, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return jbod.Snapshot{}, errors.New("no deadline")
	}
	d.deadlines <- time.Until(deadline)
	return jbod.Snapshot{Up: true}, nil
}

// TestEndToEndWithAClient wires the real client to the handler, so the two
// packages are known to fit together.
func TestEndToEndWithAClient(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := filepath.Join(root, "1:0:0:0", "Slot 01, front")
	if err := os.MkdirAll(filepath.Join(base, "device", "scsi_generic", "sg1"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := jbod.New(jbod.WithSysfs(root), jbod.WithRunner(func(_ context.Context, name string, args ...string) (string, error) {
		switch name {
		case "lsscsi":
			return "[1:0:0:0] enclosu ACME Shelf 1 - /dev/sg0\n", nil
		case "sg_inq":
			return "Vendor identification: ACME\nUnit serial number: ENC1\n", nil
		case "sg_map":
			return "/dev/sg1 /dev/sda\n", nil
		case "scsi_temperature":
			return "Current temperature: 37 C\n", nil
		case "sginfo":
			return "Revision level: FW1\n", nil
		case "sg_ses":
			if args[0] == "-j" {
				return "Fan A [2,0] Cooling\n", nil
			}
			return "speed code: 2, Actual speed: 1200 rpm, low speed\n", nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	}))
	r := get(t, New(client).Handler(), "GET", "/metrics")
	if r.Code != http.StatusOK {
		t.Fatalf("%d: %s", r.Code, r.Body.String())
	}
	for _, want := range []string{
		"number_of_enclosures 1",
		`jbod_slot_temperature{enclosure="1:0:0:0",slot="Slot 01"} 37`,
		`jbod_fan_speed_rpm{component="Fan A",component_id="2,0",enclosure="1:0:0:0",`,
		"jbod_up 1",
	} {
		if !strings.Contains(r.Body.String(), want) {
			t.Errorf("missing %q in\n%s", want, r.Body.String())
		}
	}
	// A client that cannot list enclosures is the total failure case.
	broken := jbod.New(jbod.WithSysfs(root), jbod.WithRunner(func(context.Context, string, ...string) (string, error) {
		return "", errors.New("not found")
	}))
	if r := get(t, New(broken).Handler(), "GET", "/metrics"); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d, want 503", r.Code)
	}
}

// TestExporterLogging covers what the daemon writes about its own scrapes
// (F): a total failure is an error record, and the cheap paths say why they
// were cheap.
func TestExporterLogging(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	failing := New(&collector{err: errors.New("lsscsi is missing")}, WithLogger(logger))
	if r := get(t, failing.Handler(), "GET", "/metrics"); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d", r.Code)
	}
	if got := buf.String(); !strings.Contains(got, `"level":"ERROR"`) || !strings.Contains(got, `"msg":"scrape failed"`) {
		t.Errorf("a total failure was not logged:\n%s", got)
	}

	buf.Reset()
	cached := New(&collector{snapshot: partialSnapshot()}, WithCacheTTL(time.Minute), WithLogger(logger))
	h := cached.Handler()
	get(t, h, "GET", "/metrics")
	get(t, h, "GET", "/metrics")
	if got := buf.String(); !strings.Contains(got, `"msg":"scrape served from cache"`) {
		t.Errorf("the cache hit was not logged:\n%s", got)
	}
}
