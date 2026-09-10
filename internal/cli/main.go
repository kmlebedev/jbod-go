// SPDX-License-Identifier: BSD-2-Clause
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// Command is an entry point: Run for jbod, Prometheus for
// prometheus-jbod-exporter.
type Command func(ctx context.Context, args []string, out io.Writer, c *jbod.Client) error

// Main is the body of both binaries: signal handling, the client, error
// reporting and the exit code. The two main packages were identical apart
// from the command they called (D4).
//
// Usage: os.Exit(cli.Main("jbod", cli.Run)).
func Main(name string, command Command) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return execute(ctx, name, command, os.Args[1:], os.Stdout, os.Stderr, jbod.New())
}

// execute is the testable half of Main: no signals, no process-wide state.
func execute(ctx context.Context, name string, command Command, args []string, out, errOut io.Writer, c *jbod.Client) int {
	err := command(ctx, args, out, c)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		// The flag package already printed the usage to out.
		return 0
	default:
		fmt.Fprintln(errOut, name+":", err)
		return 1
	}
}
