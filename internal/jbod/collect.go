// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"context"
	"log/slog"
	"time"
)

// Snapshot is everything one collection pass managed to read, plus what it
// failed to read. It is what internal/metrics encodes.
type Snapshot struct {
	Enclosures []Enclosure
	// Slots is every bay, empty ones included. Disks is the occupied
	// subset with its telemetry filled in.
	Slots []Slot
	Disks []Disk
	Fans  []Fan
	// Status is the per-shelf health report: what the enclosure says about
	// itself, its components and their sensors, and how much of that this
	// pass managed to read (ROADMAP 5).
	Status []EnclosureStatus
	// Errors counts failed operations per collector.
	Errors map[string]int
	// Duration is how long the pass took.
	Duration time.Duration
	// ReadAt is when the pass started, so a consumer can tell how fresh
	// the readings are rather than how fresh the scrape is.
	ReadAt time.Time
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
	p := newProblems(c.logger)
	enc, err := c.enclosures(ctx, p)
	if err != nil {
		return Snapshot{}, err
	}
	// One walk feeds both views: the disks are the occupied slots, so a
	// second walk could only disagree with the first.
	slots := c.slots(ctx, enc, p)
	disks := c.disksFromSlots(ctx, slots, DiskOptions{WithTelemetry: true}, p)
	fans := c.fans(ctx, enc, p)
	// One inspection per pass, reusing the slot walk above: the health
	// report and the metrics are two renderings of the same collection,
	// not two collections (ROADMAP 3).
	status := c.inspect(ctx, enc, slots, p)
	s := Snapshot{
		Enclosures: enc,
		Slots:      slots,
		Disks:      disks,
		Fans:       fans,
		Status:     status,
		ReadAt:     start,
		Errors:     p.snapshotCounts(),
		Duration:   time.Since(start),
		Up:         ctx.Err() == nil,
	}
	// How long a scrape takes is the number an operator needs when a shelf
	// starts missing its interval, so it is logged as well as exported. A
	// clean pass is routine and stays at debug level; a pass that lost
	// something is worth a line at info.
	failed := 0
	for _, n := range s.Errors {
		failed += n
	}
	level := slog.LevelDebug
	if failed > 0 {
		level = slog.LevelInfo
	}
	c.logger.Log(ctx, level, "collection finished",
		"duration", s.Duration, "enclosures", len(s.Enclosures), "slots", len(s.Slots),
		"disks", len(s.Disks), "fans", len(s.Fans), "components", components(s),
		"errors", failed, "up", s.Up)
	return s, nil
}

// components counts the elements of every shelf in a snapshot, for the
// collection log line.
func components(s Snapshot) int {
	n := 0
	for _, status := range s.Status {
		n += len(status.Components)
	}
	return n
}
