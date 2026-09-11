// SPDX-License-Identifier: BSD-2-Clause

// Package exporter serves collected enclosure metrics over HTTP: the scrape
// budget, the deduplication of overlapping scrapes and the optional reuse of
// a recent result live here, separate from the collection and from the
// encoding.
package exporter

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kmlebedev/jbod-go/internal/jbod"
	"github.com/kmlebedev/jbod-go/internal/metrics"
	"github.com/kmlebedev/jbod-go/internal/process"
)

// DefaultScrapeTimeout bounds one full collection pass.
const DefaultScrapeTimeout = 2 * time.Minute

// Collector is one pass over the hardware. *jbod.Client implements it.
type Collector interface {
	Collect(ctx context.Context) (jbod.Snapshot, error)
}

// Exporter serves /metrics for one Collector.
//
// Concurrent requests share a single collection pass: Prometheus and a manual
// curl must not both walk the shelf and spawn a hundred processes each (B2).
// With a non-zero cache TTL a scrape that arrives shortly after another one
// is answered from the previous result instead.
type Exporter struct {
	collector     Collector
	scrapeTimeout time.Duration
	cacheTTL      time.Duration
	logger        *slog.Logger

	mu       sync.Mutex
	totals   map[string]int
	inflight *pass
	cached   *pass
	cachedAt time.Time
	// passes counts started collection passes; requests that joined a
	// running pass or came from the cache do not increment it.
	passes int
}

// pass is one in-flight or completed collection pass.
type pass struct {
	done chan struct{}
	body string
	err  error
}

// Option configures an Exporter. Values that make no sense are ignored, so
// New always returns a usable exporter.
type Option func(*Exporter)

// WithScrapeTimeout bounds one collection pass. The default is
// DefaultScrapeTimeout.
func WithScrapeTimeout(d time.Duration) Option {
	return func(e *Exporter) {
		if d > 0 {
			e.scrapeTimeout = d
		}
	}
}

// WithCacheTTL serves the previous result for d after it was collected.
// Without it every scrape is fresh and only concurrent scrapes are shared.
func WithCacheTTL(d time.Duration) Option {
	return func(e *Exporter) {
		if d > 0 {
			e.cacheTTL = d
		}
	}
}

// WithLogger sets where the exporter reports scrapes and failures. Without
// it the exporter stays silent.
func WithLogger(l *slog.Logger) Option {
	return func(e *Exporter) {
		if l != nil {
			e.logger = l
		}
	}
}

// New returns an exporter for c with opts applied.
func New(c Collector, opts ...Option) *Exporter {
	e := &Exporter{
		collector:     c,
		scrapeTimeout: DefaultScrapeTimeout,
		logger:        slog.New(slog.DiscardHandler),
		totals:        map[string]int{},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Handler serves GET/HEAD on / and /metrics.
func (e *Exporter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
			e.logger.ErrorContext(ctx, "scrape failed", "remote", r.RemoteAddr, "err", err)
			http.Error(w, "collection failed: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if r.Method != http.MethodHead {
			fmt.Fprint(w, result)
			// The process_* metrics describe this instant, so they are
			// never served from the collection cache. The Rust exporter
			// published them through the prometheus crate, and dashboards
			// built on it need them (A9).
			fmt.Fprint(w, process.Encode())
		}
	})
}

// body returns the encoded metrics, collecting them at most once per set of
// overlapping requests.
func (e *Exporter) body(ctx context.Context) (string, error) {
	e.mu.Lock()
	if e.cacheTTL > 0 && e.cached != nil && time.Since(e.cachedAt) < e.cacheTTL {
		cached, age := e.cached, time.Since(e.cachedAt)
		e.mu.Unlock()
		e.logger.DebugContext(ctx, "scrape served from cache", "age", age, "ttl", e.cacheTTL)
		return cached.body, cached.err
	}
	if running := e.inflight; running != nil {
		e.mu.Unlock()
		e.logger.DebugContext(ctx, "scrape joined a running collection")
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

	snapshot, err := e.collector.Collect(ctx)
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
		current.body = metrics.Encode(snapshot, totals)
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
