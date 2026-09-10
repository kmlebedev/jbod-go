// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// Command is an entry point: Run for jbod, Prometheus for
// prometheus-jbod-exporter.
type Command func(ctx context.Context, args []string, out, errOut io.Writer, c *jbod.Client) error

// Main is the body of both binaries: signal handling, the client, error
// reporting and the exit code. The two main packages were identical apart
// from the command they called (D4).
//
// Usage: os.Exit(cli.Main("jbod", cli.Run)).
func Main(name string, command Command) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// A command run by a person is quiet by default: what went wrong comes
	// back as its error, and the tables already mark absent readings. The
	// exporter replaces this logger with one configured by its own flags.
	logger, err := newLogger(os.Stderr, LogFormatText, slog.LevelError)
	if err != nil {
		logger = slog.New(slog.DiscardHandler)
	}
	client := jbod.New(jbod.WithLogger(logger))
	return execute(ctx, name, command, os.Args[1:], os.Stdout, os.Stderr, client)
}

// execute is the testable half of Main: no signals, no process-wide state.
func execute(ctx context.Context, name string, command Command, args []string, out, errOut io.Writer, c *jbod.Client) int {
	err := command(ctx, args, out, errOut, c)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, pflag.ErrHelp):
		// pflag already printed the usage to out.
		return 0
	default:
		fmt.Fprintln(errOut, name+":", err)
		return 1
	}
}
