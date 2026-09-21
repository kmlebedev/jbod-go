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
	"time"

	"github.com/spf13/pflag"

	"github.com/kmlebedev/jbod-go/internal/exporter"
	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// PrometheusHelp is the usage of the exporter, for both spellings of it.
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
  --deprecated-metrics   also export the pre-1.1 series (default true)
  --log-level LEVEL      debug, info, warn or error (default info)
  --log-format FORMAT    json or text (default json)

Concurrent scrapes always share one collection pass; --cache-ttl additionally
serves a recent result without touching the hardware. Set it to about half the
Prometheus scrape_interval if the shelf is polled from several places.

jbod_fan_rpm is deprecated: its labels omit the enclosure, so identical fans
of two shelves overwrite each other. Use jbod_fan_speed_rpm, which carries
enclosure and enclosure_id, and turn the old series off with
--deprecated-metrics=false once nothing reads it. Removal is planned for 2.0.
Logs go to stderr.
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

// ipAliases lets --ip stand for --ip-address, the way the Rust original
// accepts both, without registering the same option twice and letting the
// last spelling win silently (E).
func ipAliases(_ *pflag.FlagSet, name string) pflag.NormalizedName {
	if name == "ip" {
		return "ip-address"
	}
	return pflag.NormalizedName(name)
}

// Prometheus runs the exporter in the foreground. It is both the "jbod
// prometheus" subcommand and the whole of prometheus-jbod-exporter.
func Prometheus(ctx context.Context, args []string, out, errOut io.Writer, c *jbod.Client) error {
	// The standalone binary never reaches Run, so it needs its own
	// --help/--version handling.
	if len(args) == 1 {
		switch args[0] {
		case "help", "-h", "--help":
			fmt.Fprint(out, PrometheusHelp)
			return nil
		case "-V", "--version":
			fmt.Fprintln(out, "jbod-go "+Version())
			return nil
		}
	}
	f := flags("prometheus", out)
	f.SetNormalizeFunc(ipAliases)
	ip := f.StringP("ip-address", "i", DefaultListenIP, "listen IP")
	port := f.StringP("port", "p", DefaultListenPort, "listen port")
	// Timeouts and the concurrency limit are operational knobs: what fits a
	// 12-slot shelf is not what fits a 60-slot one, and a README advising
	// "raise scrape_timeout" needs a matching flag on this side (B4).
	commandTimeout := f.Duration("command-timeout", jbod.DefaultCommandTimeout, "timeout of one external command")
	scrapeTimeout := f.Duration("scrape-timeout", exporter.DefaultScrapeTimeout, "timeout of one full collection")
	cacheTTL := f.Duration("cache-ttl", 0, "reuse the previous collection for this long")
	concurrency := f.Int("concurrency", jbod.DefaultConcurrency, "external commands allowed to run at once")
	deprecated := f.Bool("deprecated-metrics", true, "also export the pre-1.1 series, including jbod_fan_rpm")
	logLevel := f.String("log-level", "info", "log level: debug, info, warn or error")
	logFormat := f.String("log-format", LogFormatJSON, "log format: json or text")
	if err := f.Parse(args); err != nil {
		return err
	}
	// The standalone exporter also takes IP and PORT positionally, which
	// pflag reports as leftover arguments; any other count is a mistake.
	switch f.NArg() {
	case 0:
	case 2:
		*ip, *port = f.Arg(0), f.Arg(1)
	default:
		return fmt.Errorf("expected IP and PORT, got %d arguments", f.NArg())
	}
	if net.ParseIP(*ip) == nil {
		return fmt.Errorf("invalid IP address %q", *ip)
	}
	n, err := strconv.Atoi(*port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid port %q", *port)
	}
	if *commandTimeout <= 0 {
		return fmt.Errorf("invalid command timeout %s", *commandTimeout)
	}
	if *scrapeTimeout <= 0 {
		return fmt.Errorf("invalid scrape timeout %s", *scrapeTimeout)
	}
	if *cacheTTL < 0 {
		return fmt.Errorf("invalid cache TTL %s", *cacheTTL)
	}
	if *concurrency < 1 {
		return fmt.Errorf("invalid concurrency %d", *concurrency)
	}
	level, err := parseLevel(*logLevel)
	if err != nil {
		return err
	}
	logger, err := newLogger(errOut, *logFormat, level)
	if err != nil {
		return err
	}
	// A derived client, so the caller's stays untouched and nothing is
	// written to a client other goroutines are already reading (C6).
	collector := c.With(
		jbod.WithCommandTimeout(*commandTimeout),
		jbod.WithConcurrency(*concurrency),
		jbod.WithLogger(logger),
	)
	// Refuse to start on a host where collection cannot work, with the
	// full list of what is missing, instead of answering every scrape with
	// an unexplained 503 (A4).
	if err := collector.Preflight(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(*ip, *port))
	if err != nil {
		return err
	}
	if warning := wildcardWarning(*ip); warning != "" {
		fmt.Fprintln(out, warning)
		logger.Warn("listening on a wildcard address", "address", *ip)
	}
	server := &http.Server{
		Handler: exporter.New(collector,
			exporter.WithScrapeTimeout(*scrapeTimeout),
			exporter.WithCacheTTL(*cacheTTL),
			exporter.WithDeprecatedMetrics(*deprecated),
			exporter.WithLogger(logger),
		).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		WriteTimeout:      *scrapeTimeout + writeGrace,
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
			logger.Info("shutting down", "timeout", shutdownTimeout)
			shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := server.Shutdown(shutdown); err != nil {
				logger.Warn("graceful shutdown failed, closing", "err", err)
				server.Close()
			}
		case <-done:
		}
	}()
	fmt.Fprintf(out, "==> Started on %s\n", listener.Addr())
	logger.Info("started",
		"address", listener.Addr().String(), "version", Version(),
		"command_timeout", *commandTimeout, "scrape_timeout", *scrapeTimeout,
		"concurrency", *concurrency, "cache_ttl", *cacheTTL, "deprecated_metrics", *deprecated)
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		logger.Info("stopped")
		return nil
	}
	return err
}
