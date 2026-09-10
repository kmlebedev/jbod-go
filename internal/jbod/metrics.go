// SPDX-License-Identifier: BSD-2-Clause
package jbod

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultScrapeTimeout bounds one full collection pass.
const DefaultScrapeTimeout = 2 * time.Minute

func label(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(s)
}

// Snapshot is everything one collection pass managed to read, plus what it
// failed to read. It is the input of the metrics encoder.
type Snapshot struct {
	Enclosures []Enclosure
	Disks      []Disk
	Fans       []Fan
	// Errors counts failed operations per collector ("enclosures",
	// "disks", "fans").
	Errors map[string]int
	// Duration is how long the pass took.
	Duration time.Duration
	// Up is false when the pass was cut short (deadline or shutdown), so
	// the data is known to be incomplete.
	Up bool
}

// Collect gathers a fresh snapshot; a fresh one every time, so hardware that
// disappeared does not leave stale series behind.
//
// It returns an error only on a total failure — enclosure discovery itself
// did not work, which means a missing binary or a missing driver. Anything
// else is partial success: a dead sensor or an unreadable shelf is counted in
// Snapshot.Errors and the rest is still reported (B5).
func (c *Client) Collect(ctx context.Context) (Snapshot, error) {
	start := time.Now()
	p := newProblems()
	enc, err := c.enclosures(ctx, p)
	if err != nil {
		return Snapshot{}, err
	}
	disks := c.disks(ctx, enc, true, p)
	fans := c.fans(ctx, enc, p)
	return Snapshot{
		Enclosures: enc,
		Disks:      disks,
		Fans:       fans,
		Errors:     p.snapshotCounts(),
		Duration:   time.Since(start),
		Up:         ctx.Err() == nil,
	}, nil
}

// Metrics collects a snapshot and encodes it in text format 0.0.4.
func (c *Client) Metrics(ctx context.Context) (string, error) {
	s, err := c.Collect(ctx)
	if err != nil {
		return "", err
	}
	return encode(s, s.Errors), nil
}

// encode renders the snapshot. errorTotals carries the cumulative
// per-collector failure counts, because jbod_scrape_errors_total is a counter
// and must not go backwards between scrapes.
func encode(s Snapshot, errorTotals map[string]int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP number_of_enclosures Number of enclosures\n# TYPE number_of_enclosures gauge\nnumber_of_enclosures %d\n", len(s.Enclosures))
	b.WriteString("# HELP jbod_slot_temperature Enclosure number, slot position and temperature\n# TYPE jbod_slot_temperature gauge\n")
	// Match the original gauge-vector behavior: the last value wins for duplicate labels.
	temps := map[string]int64{}
	var tempKeys []string
	for _, d := range s.Disks {
		n, e := strconv.ParseInt(d.Temperature, 10, 64)
		if e != nil {
			continue
		}
		key := fmt.Sprintf("slot=\"%s\",enclosure=\"%s\"", label(d.Slot), label(d.Enclosure))
		if _, ok := temps[key]; !ok {
			tempKeys = append(tempKeys, key)
		}
		temps[key] = n
	}
	for _, key := range tempKeys {
		fmt.Fprintf(&b, "jbod_slot_temperature{%s} %d\n", key, temps[key])
	}
	b.WriteString("# HELP jbod_fan_rpm The RPM speed of FAN components, device and slot\n# TYPE jbod_fan_rpm gauge\n")
	speeds := map[string]int64{}
	var fanKeys []string
	for _, f := range s.Fans {
		key := fmt.Sprintf("device=\"%s\",slot=\"%s\"", label(f.Description), label(f.Index))
		if _, ok := speeds[key]; !ok {
			fanKeys = append(fanKeys, key)
		}
		speeds[key] = f.Speed
	}
	for _, key := range fanKeys {
		fmt.Fprintf(&b, "jbod_fan_rpm{%s} %d\n", key, speeds[key])
	}
	// Health of the scrape itself: a partial collection is reported through
	// these series instead of an HTTP error.
	up := 0
	if s.Up {
		up = 1
	}
	fmt.Fprintf(&b, "# HELP jbod_up Whether the last collection completed\n# TYPE jbod_up gauge\njbod_up %d\n", up)
	b.WriteString("# HELP jbod_scrape_duration_seconds Duration of the last collection\n# TYPE jbod_scrape_duration_seconds gauge\n")
	fmt.Fprintf(&b, "jbod_scrape_duration_seconds %s\n", strconv.FormatFloat(s.Duration.Seconds(), 'f', 3, 64))
	b.WriteString("# HELP jbod_scrape_errors_total Failed collection operations per collector\n# TYPE jbod_scrape_errors_total counter\n")
	// Always emit every collector so the series exist from the first scrape.
	collectors := []string{collectorEnclosures, collectorDisks, collectorFans}
	for name := range errorTotals {
		if name != collectorEnclosures && name != collectorDisks && name != collectorFans {
			collectors = append(collectors, name)
		}
	}
	slices.Sort(collectors[3:])
	for _, name := range collectors {
		fmt.Fprintf(&b, "jbod_scrape_errors_total{collector=\"%s\"} %d\n", label(name), errorTotals[name])
	}
	return b.String()
}

// Exporter serves /metrics for one Client.
//
// Concurrent requests share a single collection pass: Prometheus and a manual
// curl must not both walk the shelf and spawn a hundred processes each (B2).
// With a non-zero cache TTL a scrape that arrives shortly after another one
// is answered from the previous result instead.
type Exporter struct {
	client        *Client
	scrapeTimeout time.Duration
	cacheTTL      time.Duration

	mu       sync.Mutex
	totals   map[string]int
	inflight *pass
	cached   *pass
	cachedAt time.Time
	// passes counts started collection passes; concurrent requests that
	// joined a running pass do not increment it.
	passes int
}

// pass is one in-flight or completed collection pass.
type pass struct {
	done chan struct{}
	body string
	err  error
}

// NewExporter returns an exporter for c. A zero scrapeTimeout means
// DefaultScrapeTimeout; a zero cacheTTL disables reuse of the previous
// result, leaving only the deduplication of concurrent scrapes.
func NewExporter(c *Client, scrapeTimeout, cacheTTL time.Duration) *Exporter {
	if scrapeTimeout <= 0 {
		scrapeTimeout = DefaultScrapeTimeout
	}
	if cacheTTL < 0 {
		cacheTTL = 0
	}
	return &Exporter{client: c, scrapeTimeout: scrapeTimeout, cacheTTL: cacheTTL, totals: map[string]int{}}
}

// Handler serves /metrics with the default timeouts and no caching.
func (c *Client) Handler() http.Handler {
	return NewExporter(c, DefaultScrapeTimeout, 0).Handler()
}

func (e *Exporter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", 405)
			return
		}
		if r.URL.Path == "/" {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), e.scrapeTimeout)
		defer cancel()
		result, err := e.body(ctx)
		if err != nil {
			// Only a total failure gets an HTTP error; partial results are
			// reported through jbod_up and jbod_scrape_errors_total.
			http.Error(w, "collection failed: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if r.Method != http.MethodHead {
			fmt.Fprint(w, result)
		}
	})
}

// body returns the encoded metrics, collecting them at most once per set of
// overlapping requests.
func (e *Exporter) body(ctx context.Context) (string, error) {
	e.mu.Lock()
	if e.cacheTTL > 0 && e.cached != nil && time.Since(e.cachedAt) < e.cacheTTL {
		cached := e.cached
		e.mu.Unlock()
		return cached.body, cached.err
	}
	if running := e.inflight; running != nil {
		e.mu.Unlock()
		// Wait for the pass that is already talking to the hardware. Its
		// deadline governs, so a request that joins late can be answered
		// with the leader's error; that is the price of not doubling the
		// load on the expander.
		select {
		case <-running.done:
			return running.body, running.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	current := &pass{done: make(chan struct{})}
	e.inflight = current
	e.passes++
	e.mu.Unlock()

	snapshot, err := e.client.Collect(ctx)
	e.mu.Lock()
	if err != nil {
		current.err = err
	} else {
		for name, n := range snapshot.Errors {
			e.totals[name] += n
		}
		totals := make(map[string]int, len(e.totals))
		for name, n := range e.totals {
			totals[name] = n
		}
		current.body = encode(snapshot, totals)
	}
	e.inflight = nil
	if err == nil {
		// A failed pass is not cached: the next scrape retries the hardware.
		e.cached, e.cachedAt = current, time.Now()
	}
	e.mu.Unlock()
	close(current.done)
	return current.body, current.err
}

// Passes reports how many collection passes actually ran. Requests that
// joined a running pass or were served from the cache are not counted.
func (e *Exporter) Passes() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.passes
}
