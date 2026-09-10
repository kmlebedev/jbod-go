package cli

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// syncBuffer is written by the exporter goroutine and read by the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestWildcardWarning(t *testing.T) {
	t.Parallel()
	if DefaultListenIP != "127.0.0.1" {
		t.Fatalf("default listen address is %s, want loopback", DefaultListenIP)
	}
	for _, ip := range []string{"0.0.0.0", "::"} {
		if wildcardWarning(ip) == "" {
			t.Errorf("no warning for wildcard %s", ip)
		}
	}
	for _, ip := range []string{"127.0.0.1", "::1", "10.0.0.1"} {
		if w := wildcardWarning(ip); w != "" {
			t.Errorf("%s: unexpected warning %q", ip, w)
		}
	}
}

func TestPrometheusRejectsBadTuning(t *testing.T) {
	t.Parallel()
	c := jbod.New(jbod.WithRunner(func(context.Context, string, ...string) (string, error) {
		t.Error("unexpected hardware access")
		return "", nil
	}))
	for _, args := range [][]string{
		{"--command-timeout", "0"},
		{"--command-timeout", "-1s"},
		{"--command-timeout", "nonsense"},
		{"--scrape-timeout", "0s"},
		{"--cache-ttl", "-1s"},
		{"--concurrency", "0"},
		{"--concurrency", "-4"},
	} {
		if err := Prometheus(context.Background(), args, &bytes.Buffer{}, c); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

// freePort returns a port that was free a moment ago.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// waitFor polls until cond holds, so the test does not depend on how fast the
// server comes up.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestShutdownCancelsScrape is the B3 regression: without BaseContext the
// collection in flight only notices the shutdown when the connection is
// closed, so SIGTERM waited out the whole command timeout with sg_ses still
// running.
func TestShutdownCancelsScrape(t *testing.T) {
	t.Parallel()
	scraping := make(chan struct{})
	cancelled := make(chan error, 1)
	var once sync.Once
	c := jbod.New(jbod.WithSysfs(t.TempDir()), jbod.WithRunner(func(ctx context.Context, _ string, _ ...string) (string, error) {
		once.Do(func() { close(scraping) })
		<-ctx.Done()
		select {
		case cancelled <- ctx.Err():
		default:
		}
		return "", ctx.Err()
	}))
	port := freePort(t)
	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		// A long command timeout: only the context cancellation can end the
		// scrape in time.
		served <- Prometheus(ctx, []string{"-i", "127.0.0.1", "-p", port, "--command-timeout", "60s"}, out, c)
	}()
	waitFor(t, "the exporter to start", func() bool { return strings.Contains(out.String(), "==> Started on") })
	if strings.Contains(out.String(), "Warning") {
		t.Fatalf("loopback bind warned: %q", out.String())
	}
	go func() {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%s/metrics", port))
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-scraping:
	case <-time.After(10 * time.Second):
		t.Fatal("scrape never reached the hardware")
	}
	cancel()
	select {
	case err := <-cancelled:
		if err == nil {
			t.Fatal("command context was not cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not reach the running command")
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("exporter returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("exporter did not return after shutdown")
	}
}

// TestPrometheusTuningReachesTheCommands checks that --command-timeout ends up
// on the client the handler collects with. The client is immutable, so the
// flag is observable where it matters: in the deadline the command is given.
func TestPrometheusTuningReachesTheCommands(t *testing.T) {
	t.Parallel()
	budgets := make(chan time.Duration, 8)
	c := jbod.New(jbod.WithSysfs(t.TempDir()), jbod.WithRunner(func(ctx context.Context, _ string, _ ...string) (string, error) {
		if deadline, ok := ctx.Deadline(); ok {
			select {
			case budgets <- time.Until(deadline):
			default:
			}
		}
		return "", nil
	}))
	port := freePort(t)
	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- Prometheus(ctx, []string{
			"--ip-address", "127.0.0.1", "--port", port,
			"--command-timeout", "3s", "--scrape-timeout", "9s",
			"--concurrency", "4", "--cache-ttl", "30s",
		}, out, c)
	}()
	waitFor(t, "the exporter to start", func() bool { return strings.Contains(out.String(), "==> Started on") })
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%s/metrics", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: %s", resp.Status)
	}
	cancel()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	select {
	case budget := <-budgets:
		// 3s from the flag, not the 15s default and not the scrape budget.
		if budget > 3*time.Second || budget < 2*time.Second {
			t.Fatalf("command budget %s, want about 3s", budget)
		}
	default:
		t.Fatal("no command ran")
	}
	// The caller's client keeps its own defaults: the exporter collects with
	// a derived copy (C6).
	deadlines := make(chan time.Duration, 1)
	probe := c.With(jbod.WithRunner(func(ctx context.Context, _ string, _ ...string) (string, error) {
		deadline, _ := ctx.Deadline()
		deadlines <- time.Until(deadline)
		return "", nil
	}))
	if _, err := probe.Enclosures(context.Background()); err != nil {
		t.Fatal(err)
	}
	if budget := <-deadlines; budget < jbod.DefaultCommandTimeout-time.Second {
		t.Fatalf("original client was mutated: command budget %s", budget)
	}
}
