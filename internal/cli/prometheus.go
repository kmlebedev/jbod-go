// SPDX-License-Identifier: BSD-2-Clause
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kmlebedev/jbod-go/internal/exporter"
	"github.com/kmlebedev/jbod-go/internal/jbod"
)

const PrometheusHelp = `prometheus-jbod-exporter - Prometheus exporter for storage enclosures (Go)
Usage:
  prometheus-jbod-exporter [-i|--ip-address IP] [-p|--port PORT] [tuning flags]
  prometheus-jbod-exporter IP PORT

Runs in the foreground (default 127.0.0.1:9945); GET /metrics serves the metrics.

Tuning flags:
  --command-timeout DUR  timeout of one external command (default 15s)
  --scrape-timeout DUR   timeout of one full collection (default 2m0s)
  --concurrency N        external commands allowed to run at once (default 12)
  --cache-ttl DUR        reuse the previous collection for this long (default 0s)

Concurrent scrapes always share one collection pass; --cache-ttl additionally
serves a recent result without touching the hardware. Set it to about half the
Prometheus scrape_interval if the shelf is polled from several places.
`

// Listen defaults and derived timeouts of the exporter.
const (
	// DefaultListenIP is the loopback address on purpose: the exporter has
	// access to /dev/sg* and normally runs as root, so exposing it to the
	// whole network should be a deliberate choice (B6).
	DefaultListenIP   = "127.0.0.1"
	DefaultListenPort = "9945"
	// shutdownTimeout bounds the graceful stop after SIGINT/SIGTERM.
	shutdownTimeout = 5 * time.Second
	// writeGrace is how much longer than a full collection a response may
	// take before the write times out.
	writeGrace = 10 * time.Second
)

// wildcardWarning returns a warning for a wildcard bind address, or "" for a
// specific one. A process that can read /dev/sg* and write LEDs should not
// reach the whole network by accident (B6).
func wildcardWarning(ip string) string {
	if parsed := net.ParseIP(ip); parsed == nil || !parsed.IsUnspecified() {
		return ""
	}
	return fmt.Sprintf("==> Warning: %s exposes enclosure telemetry on every interface; bind %s unless that is intended", ip, DefaultListenIP)
}

// Prometheus runs the exporter in the foreground. It is both the "jbod
// prometheus" subcommand and the whole of prometheus-jbod-exporter.
func Prometheus(ctx context.Context, args []string, out io.Writer, c *jbod.Client) error {
	// The standalone binary never reaches Run, so it needs its own
	// --help/--version handling.
	if len(args) == 1 {
		switch args[0] {
		case "help", "-h", "--help":
			fmt.Fprint(out, PrometheusHelp)
			return nil
		case "-V", "--version":
			fmt.Fprintln(out, "jbod-go "+Version)
			return nil
		}
	}
	f := flags("prometheus", out)
	ip, port := DefaultListenIP, DefaultListenPort
	for _, key := range []string{"i", "ip", "ip-address"} {
		f.StringVar(&ip, key, ip, "listen IP")
	}
	for _, key := range []string{"p", "port"} {
		f.StringVar(&port, key, port, "listen port")
	}
	// Timeouts and the concurrency limit are operational knobs: what fits a
	// 12-slot shelf is not what fits a 60-slot one, and a README advising
	// "raise scrape_timeout" needs a matching flag on this side (B4).
	commandTimeout := jbod.DefaultCommandTimeout
	concurrency := jbod.DefaultConcurrency
	scrapeTimeout := exporter.DefaultScrapeTimeout
	var cacheTTL time.Duration
	f.DurationVar(&commandTimeout, "command-timeout", commandTimeout, "timeout of one external command")
	f.DurationVar(&scrapeTimeout, "scrape-timeout", scrapeTimeout, "timeout of one full collection")
	f.DurationVar(&cacheTTL, "cache-ttl", cacheTTL, "reuse the previous collection for this long")
	f.IntVar(&concurrency, "concurrency", concurrency, "external commands allowed to run at once")
	// Keep standalone exporter's positional IP PORT invocation.
	if len(args) == 2 && !strings.HasPrefix(args[0], "-") {
		ip, port = args[0], args[1]
	} else {
		if err := f.Parse(args); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return errors.New("unexpected exporter arguments")
		}
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid IP address %q", ip)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid port %q", port)
	}
	if commandTimeout <= 0 {
		return fmt.Errorf("invalid command timeout %s", commandTimeout)
	}
	if scrapeTimeout <= 0 {
		return fmt.Errorf("invalid scrape timeout %s", scrapeTimeout)
	}
	if cacheTTL < 0 {
		return fmt.Errorf("invalid cache TTL %s", cacheTTL)
	}
	if concurrency < 1 {
		return fmt.Errorf("invalid concurrency %d", concurrency)
	}
	// A derived client, so the caller's stays untouched and nothing is
	// written to a client other goroutines are already reading (C6).
	collector := c.With(jbod.WithCommandTimeout(commandTimeout), jbod.WithConcurrency(concurrency))
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, port))
	if err != nil {
		return err
	}
	if warning := wildcardWarning(ip); warning != "" {
		fmt.Fprintln(out, warning)
	}
	server := &http.Server{
		Handler:           exporter.New(collector, scrapeTimeout, cacheTTL).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		WriteTimeout:      scrapeTimeout + writeGrace,
		// Without BaseContext a scrape in flight only sees the cancellation
		// when the connection is closed, so SIGTERM would wait out the whole
		// shutdown timeout with sg_ses still running (B3).
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := server.Shutdown(shutdown); err != nil {
				server.Close()
			}
		case <-done:
		}
	}()
	fmt.Fprintf(out, "==> Started on %s\n", listener.Addr())
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
