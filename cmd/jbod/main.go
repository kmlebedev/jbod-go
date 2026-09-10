package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"jbod-go/internal/cli"
	"jbod-go/internal/jbod"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := cli.Run(ctx, os.Args[1:], os.Stdout, jbod.New()); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "jbod:", err)
		os.Exit(1)
	}
}
