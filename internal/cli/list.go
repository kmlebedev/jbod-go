// SPDX-License-Identifier: BSD-2-Clause
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// expandShortFlags splits clap-style combined short options ("-ed" into
// "-e -d") for the flags in alphabet, which the standard flag package does
// not do. Anything else is passed through untouched, including "--long"
// options and bare arguments.
func expandShortFlags(alphabet string, args []string) []string {
	expanded := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 2 && strings.Trim(a[1:], alphabet) == "" {
			for _, ch := range a[1:] {
				expanded = append(expanded, "-"+string(ch))
			}
			continue
		}
		expanded = append(expanded, a)
	}
	return expanded
}

func cmdList(ctx context.Context, args []string, out io.Writer, inv Inventory) error {
	f := flags("list", out)
	var enc, disks, fans bool
	boolean(f, &enc, "e", "enclosure")
	boolean(f, &disks, "d", "disks")
	boolean(f, &fans, "f", "fan")
	if err := f.Parse(expandShortFlags("edf", args)); err != nil {
		return err
	}
	if f.NArg() != 0 || (!enc && !disks && !fans) {
		return errors.New("list requires --enclosure, --disks or --fan")
	}
	enclosures, err := inv.Enclosures(ctx)
	if err != nil {
		return err
	}
	// Each flag selects an independent section: -e -f must print both.
	// Sections are flushed separately so tabwriter does not align the
	// enclosure columns against the fan columns.
	printed := false
	if enc || disks {
		var ds []jbod.Disk
		if disks {
			ds, err = inv.Disks(ctx, enclosures, jbod.DiskOptions{WithTelemetry: true})
			if err != nil {
				return err
			}
		}
		if err := printEnclosures(out, enclosures, ds); err != nil {
			return err
		}
		printed = true
	}
	if fans {
		fs, err := inv.Fans(ctx, enclosures)
		if err != nil {
			return err
		}
		if printed {
			fmt.Fprintln(out)
		}
		if err := printFans(out, fs); err != nil {
			return err
		}
	}
	return nil
}
