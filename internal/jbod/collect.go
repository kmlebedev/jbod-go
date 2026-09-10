// SPDX-License-Identifier: BSD-2-Clause
package jbod

import (
	"context"
	"time"
)

// Snapshot is everything one collection pass managed to read, plus what it
// failed to read. It is what internal/metrics encodes.
type Snapshot struct {
	Enclosures []Enclosure
	Disks      []Disk
	Fans       []Fan
	// Errors counts failed operations per collector.
	Errors map[string]int
	// Duration is how long the pass took.
	Duration time.Duration
	// Up is false when the pass was cut short (deadline or shutdown), so
	// the data is known to be incomplete.
	Up bool
}

// Collect gathers a fresh snapshot; a fresh one every time, so hardware that
// disappeared does not leave stale series behind.
//
// It returns an error only on a total failure — enclosure discovery itself
// did not work, which means a missing binary or a missing driver. Anything
// else is partial success: a dead sensor or an unreadable shelf is counted in
// Snapshot.Errors and the rest is still reported (B5).
func (c *Client) Collect(ctx context.Context) (Snapshot, error) {
	start := time.Now()
	p := newProblems()
	enc, err := c.enclosures(ctx, p)
	if err != nil {
		return Snapshot{}, err
	}
	disks := c.disks(ctx, enc, DiskOptions{WithTelemetry: true}, p)
	fans := c.fans(ctx, enc, p)
	return Snapshot{
		Enclosures: enc,
		Disks:      disks,
		Fans:       fans,
		Errors:     p.snapshotCounts(),
		Duration:   time.Since(start),
		Up:         ctx.Err() == nil,
	}, nil
}
