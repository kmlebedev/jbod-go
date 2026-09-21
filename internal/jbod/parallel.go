// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// forEach runs fn for indices [0,n) with at most limit goroutines in flight.
//
// A 60-slot shelf needs two external commands per disk plus one per fan, so
// running them one after another cannot fit into a scrape interval. There is
// no dependency between the calls, so a plain counting semaphore is enough
// and keeps the module dependency-free (no errgroup).
//
// fn must only touch storage indexed by i, or storage that is safe for
// concurrent use: forEach itself does no locking. It always waits for every
// started task, so the caller may read the whole result slice afterwards.
// When ctx is already done the remaining tasks are skipped instead of being
// started; the commands they would run would fail anyway.
func forEach(ctx context.Context, limit, n int, fn func(i int)) {
	if n <= 0 {
		return
	}
	if limit < 1 {
		limit = 1
	}
	if limit > n {
		limit = n
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range n {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

// Collector names that appear as keys in Snapshot.Errors, and the order in
// which the metrics encoder renders them.
const (
	CollectorEnclosures = "enclosures"
	// CollectorSlots covers the sysfs walk that enumerates the bays. Disk
	// enumeration is a projection of it, so an unreadable shelf is counted
	// here and only the per-disk telemetry is counted as disks.
	CollectorSlots = "slots"
	CollectorDisks = "disks"
	CollectorFans  = "fans"
	// CollectorLED covers the LED writes and their readback.
	CollectorLED = "led"
)

// Collectors lists every collector, so the error series exist from the first
// scrape even when nothing failed.
var Collectors = []string{CollectorEnclosures, CollectorSlots, CollectorDisks, CollectorFans, CollectorLED}

// problems accumulates the failures of a single collection pass.
//
// A scrape that touches a hundred devices will eventually hit a broken
// sensor, and losing the whole shelf over one bad SES index is exactly the
// failure mode B5 is about. Every failure is counted per collector so the
// exporter can publish it; only failures recorded with fail are also
// reported to the strict callers (the CLI), which still refuse to print a
// half-truth. note records a failure that the data model already expresses
// with a sentinel value ("ERR", "N/A", "NONE"), so the CLI keeps printing.
type problems struct {
	mu     sync.Mutex
	counts map[string]int
	fatal  []error
	log    *slog.Logger
}

func newProblems(log *slog.Logger) *problems {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &problems{counts: map[string]int{}, log: log}
}

// note counts a tolerated failure: the caller already left the value absent.
func (p *problems) note(collector string, err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	p.counts[collector]++
	p.mu.Unlock()
	// Every counted failure is logged here, once, so a shelf that quietly
	// degrades leaves a trail and not just a counter.
	p.log.Warn("collection error", "collector", collector, "tolerated", true, "err", err)
}

// fail counts a failure and remembers it for callers that want strict errors.
func (p *problems) fail(collector string, err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	p.counts[collector]++
	p.fatal = append(p.fatal, err)
	p.mu.Unlock()
	p.log.Warn("collection error", "collector", collector, "tolerated", false, "err", err)
}

// err joins the failures recorded with fail, or nil if there were none.
func (p *problems) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return errors.Join(p.fatal...)
}

// snapshotCounts copies the per-collector failure counts.
func (p *problems) snapshotCounts() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]int, len(p.counts))
	for k, v := range p.counts {
		out[k] = v
	}
	return out
}
