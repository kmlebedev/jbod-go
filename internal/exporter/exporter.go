// SPDX-License-Identifier: BSD-2-Clause

// Package exporter serves collected enclosure metrics over HTTP: the scrape
// budget, the deduplication of overlapping scrapes and the optional reuse of
// a recent result live here, separate from the collection and from the
// exposition.
//
// The exposition itself belongs to github.com/prometheus/client_golang: a
// registry gathers the snapshot collector together with the process, Go
// runtime and build-info collectors, and promhttp writes the response. That
// is where content negotiation, OpenMetrics, compression, escaping and the
// handler's own telemetry come from, so none of it is maintained here.
package exporter

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"

	"github.com/kmlebedev/jbod-go/internal/jbod"
	"github.com/kmlebedev/jbod-go/internal/metrics"
)

// DefaultScrapeTimeout bounds one full collection pass.
const DefaultScrapeTimeout = 2 * time.Minute

// Collector is one pass over the hardware. *jbod.Client implements it.
//
// It is not a prometheus.Collector: a pass takes minutes, talks to
// /dev/sg*, and has to be cancelled when the scrape deadline expires or the
// daemon is stopped, and prometheus.Collector.Collect has nowhere to put a
// context (B3). The hardware pass therefore happens in the handler, and
// what reaches the registry is a snapshot that is already in memory.
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
	encode        metrics.Options
	version       string
	logger        *slog.Logger

	// registry holds everything that is not the snapshot: the process and
	// Go runtime collectors that the Rust exporter got from the prometheus
	// crate's "process" feature (A9), the build info, and promhttp's own
	// counters. They are read live on every scrape and are never cached.
	registry *prometheus.Registry
	handler  http.Handler

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
	done     chan struct{}
	snapshot jbod.Snapshot
	// totals is the cumulative error count as of this pass. It is a copy,
	// because jbod_scrape_errors_total must not go backwards and a cached
	// pass keeps publishing the counts it was collected with.
	totals map[string]int
	err    error
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

// WithVersion labels jbod_build_info with the version of the running
// binary. Without it the label says "unknown".
func WithVersion(v string) Option {
	return func(e *Exporter) {
		if v != "" {
			e.version = v
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
		encode:        metrics.Options{},
		version:       "unknown",
		logger:        slog.New(slog.DiscardHandler),
		totals:        map[string]int{},
		registry:      prometheus.NewRegistry(),
	}
	for _, opt := range opts {
		opt(e)
	}
	e.registry.MustRegister(
		// process_cpu_seconds_total and friends, straight from the client
		// library. The hand-written /proc reader this replaces existed
		// only because the Go port had dropped them (A9).
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		// go_* is the exporter's own health: a leaked goroutine per
		// scrape is exactly the kind of bug a forked sg_ses causes.
		collectors.NewGoCollector(),
		buildInfo(e.version),
	)
	// InstrumentMetricHandler adds promhttp_metric_handler_requests_total,
	// so a scrape that fails is visible in the exporter's own metrics and
	// not only in the log.
	e.handler = promhttp.InstrumentMetricHandler(e.registry, http.HandlerFunc(e.serve))
	return e
}

// buildInfo is the exporter's own identity, the series every Prometheus
// deployment expects to be able to join a version onto.
//
// prometheus/common/version publishes the same thing from package-level
// variables that a build stamps with -X. This exporter already carries its
// version in one place, so the collector is built here instead of writing
// that version into a global at start-up — a process-wide write is not
// something a constructor should do, least of all one a test can call twice
// in parallel.
func buildInfo(version string) prometheus.Collector {
	return prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "jbod",
		Name:      "build_info",
		Help:      "Version and toolchain of the running exporter; the value is always 1",
		ConstLabels: prometheus.Labels{
			"version":   version,
			"goversion": runtime.Version(),
			"goos":      runtime.GOOS,
			"goarch":    runtime.GOARCH,
		},
	}, func() float64 { return 1 })
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
		e.handler.ServeHTTP(w, r)
	})
}

// serve answers one scrape: collect (or reuse) a snapshot, then let promhttp
// render it together with the process and runtime metrics.
func (e *Exporter) serve(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), e.scrapeTimeout)
	defer cancel()
	result, err := e.snapshot(ctx)
	if err != nil {
		// Only a total failure gets an HTTP error; partial results are
		// reported through jbod_up and jbod_scrape_errors_total.
		e.logger.ErrorContext(ctx, "scrape failed", "remote", r.RemoteAddr, "err", err)
		http.Error(w, "collection failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodHead {
		// A probe gets the headers without paying for the encoding. The
		// collection above still ran, so a broken shelf is still a 503.
		w.Header().Set("Content-Type", string(expfmt.NewFormat(expfmt.TypeTextPlain)))
		return
	}
	// A registry per scrape, holding the snapshot that this request is
	// answered from. It is the pattern the blackbox and SNMP exporters use
	// for per-request results, and it is what keeps two scrapes that ended
	// up with different snapshots — a fresh one and a cached one — from
	// writing into each other's response.
	scrape := prometheus.NewRegistry()
	scrape.MustRegister(metrics.NewCollector(result.snapshot, result.totals, e.encode))
	promhttp.HandlerFor(
		prometheus.Gatherers{e.registry, scrape},
		promhttp.HandlerOpts{
			ErrorLog: slogLogger{ctx: r.Context(), logger: e.logger},
			// A series that could not be built must not cost the whole
			// scrape: the rest of the shelf is still worth having (B5).
			ErrorHandling:     promhttp.ContinueOnError,
			Registry:          e.registry,
			EnableOpenMetrics: true,
		},
	).ServeHTTP(w, r)
}

// slogLogger adapts the exporter's logger to promhttp.Logger.
type slogLogger struct {
	ctx    context.Context
	logger *slog.Logger
}

func (l slogLogger) Println(v ...any) {
	l.logger.ErrorContext(l.ctx, "metrics handler error", "err", fmt.Sprint(v...))
}

// snapshot returns the data one response is built from, collecting it at
// most once per set of overlapping requests.
func (e *Exporter) snapshot(ctx context.Context) (*pass, error) {
	e.mu.Lock()
	if e.cacheTTL > 0 && e.cached != nil && time.Since(e.cachedAt) < e.cacheTTL {
		cached, age := e.cached, time.Since(e.cachedAt)
		e.mu.Unlock()
		e.logger.DebugContext(ctx, "scrape served from cache", "age", age, "ttl", e.cacheTTL)
		return cached, cached.err
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
			return running, running.err
		case <-ctx.Done():
			return nil, ctx.Err()
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
		current.snapshot = snapshot
		current.totals = maps.Clone(e.totals)
	}
	e.inflight = nil
	if err == nil {
		// A failed pass is not cached: the next scrape retries the hardware.
		e.cached, e.cachedAt = current, time.Now()
	}
	e.mu.Unlock()
	close(current.done)
	return current, current.err
}

// Passes reports how many collection passes actually ran. Requests that
// joined a running pass or were served from the cache are not counted.
func (e *Exporter) Passes() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.passes
}
