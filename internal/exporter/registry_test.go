package exporter

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// TestExporterPublishesItsOwnHealth covers what moved to the client library
// with the refactoring: the process metrics the Rust exporter got from the
// prometheus crate (A9), the Go runtime metrics, the build info and the
// handler's own request counter. They are the exporter's own health and are
// never served from the collection cache.
func TestExporterPublishesItsOwnHealth(t *testing.T) {
	t.Parallel()
	h := New(&collector{snapshot: partialSnapshot()}).Handler()
	get(t, h, "GET", "/metrics")
	body := get(t, h, "GET", "/metrics").Body.String()

	want := []string{
		"\ngo_goroutines ",
		"jbod_build_info{",
		// The counter is incremented by the handler itself, so the second
		// scrape sees the first one.
		`promhttp_metric_handler_requests_total{code="200"} 1`,
	}
	if runtime.GOOS == "linux" {
		want = append(want, "\nprocess_cpu_seconds_total ", "\nprocess_open_fds ")
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q in\n%s", w, body)
		}
	}
}

// TestOpenMetricsNegotiation checks the second exposition format, which the
// exporter gets for free from promhttp and which Prometheus asks for when
// it wants exemplars or a created timestamp.
func TestOpenMetricsNegotiation(t *testing.T) {
	t.Parallel()
	h := New(&collector{snapshot: partialSnapshot()}).Handler()
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "application/openmetrics-text") {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Type"))
	}
	// OpenMetrics ends with an explicit marker; a truncated body does not.
	if !strings.HasSuffix(w.Body.String(), "# EOF\n") {
		t.Errorf("not an OpenMetrics body:\n%s", w.Body.String())
	}
}

// TestCompressionIsNegotiated is the other thing promhttp brings: a full
// shelf is a large text body, and a scrape that says it accepts gzip gets
// a compressed one.
func TestCompressionIsNegotiated(t *testing.T) {
	t.Parallel()
	h := New(&collector{snapshot: partialSnapshot()}).Handler()
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	if strings.Contains(w.Body.String(), "jbod_up") {
		t.Error("the body is not compressed")
	}
}
