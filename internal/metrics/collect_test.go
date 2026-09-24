package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/expfmt"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// encode renders a snapshot the way the exporter does: through a registry,
// in text format 0.0.4. The assertions in this package are written against
// that output, because it is what Prometheus reads.
//
// Gathering also enforces what the registry enforces — no duplicate series,
// no label set that disagrees with its descriptor — so a collector bug is a
// test failure here rather than an empty scrape in production.
func encode(t *testing.T, s jbod.Snapshot, errorTotals map[string]int, opts Options) string {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewCollector(s, errorTotals, opts))
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var b strings.Builder
	enc := expfmt.NewEncoder(&b, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, f := range families {
		if err := enc.Encode(f); err != nil {
			t.Fatalf("encode %s: %v", f.GetName(), err)
		}
	}
	return b.String()
}

// TestCollectorDescribesWhatItPublishes is what makes the collector a
// checked one: every series it emits must have been announced, or the
// registry drops the whole scrape.
func TestCollectorDescribesWhatItPublishes(t *testing.T) {
	t.Parallel()
	described := make(chan *prometheus.Desc, len(descriptors)+1)
	c := NewCollector(fullSnapshot(), nil, Options{})
	c.Describe(described)
	close(described)
	known := map[*prometheus.Desc]struct{}{}
	for d := range described {
		known[d] = struct{}{}
	}
	if len(known) != len(descriptors) {
		t.Errorf("Describe sent %d descriptors, want %d", len(known), len(descriptors))
	}
	collected := make(chan prometheus.Metric, 4096)
	c.Collect(collected)
	close(collected)
	for m := range collected {
		if _, ok := known[m.Desc()]; !ok {
			t.Errorf("%s was collected but never described", m.Desc())
		}
	}
}

// TestExpositionIsLintClean runs the client library's own linter over the
// output, which is where the Prometheus naming rules live.
//
// The names below are known and deliberate: number_of_enclosures is the
// Rust exporter's name and renaming it would break every dashboard, and
// jbod_slot_temperature predates the unit suffix. Anything else the linter
// finds is a new mistake.
func TestExpositionIsLintClean(t *testing.T) {
	t.Parallel()
	problems, err := testutil.CollectAndLint(NewCollector(fullSnapshot(), nil, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"number_of_enclosures": true, "jbod_slot_temperature": true}
	for _, p := range problems {
		if allowed[p.Metric] {
			continue
		}
		t.Errorf("%s: %s", p.Metric, p.Text)
	}
}

// TestCollectAndCompare pins one family down to the byte, using the
// comparison helper the client library ships for exporters.
func TestCollectAndCompare(t *testing.T) {
	t.Parallel()
	const want = `# HELP jbod_up Whether the last collection completed
# TYPE jbod_up gauge
jbod_up 1
`
	if err := testutil.CollectAndCompare(
		NewCollector(fullSnapshot(), nil, Options{}),
		strings.NewReader(want), "jbod_up",
	); err != nil {
		t.Error(err)
	}
}
