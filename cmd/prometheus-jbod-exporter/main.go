// SPDX-License-Identifier: BSD-2-Clause

package main

import (
	"os"

	"github.com/kmlebedev/jbod-go/internal/cli"
)

func main() { os.Exit(cli.Main("exporter", cli.Prometheus)) }
