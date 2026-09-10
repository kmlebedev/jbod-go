package jbod

import (
	"context"
	"testing"
	"time"
)

// TestNewDefaults covers C7: an empty sysfs root used to be a working state
// that made filepath.Join produce relative paths, and a test passed by
// accident. New is the only entry point and always yields a usable client.
func TestNewDefaults(t *testing.T) {
	t.Parallel()
	c := New()
	if c.sysfs != DefaultSysfs {
		t.Errorf("sysfs = %q, want %q", c.sysfs, DefaultSysfs)
	}
	if c.commandTimeout != DefaultCommandTimeout || c.concurrency != DefaultConcurrency {
		t.Errorf("timeout %s, concurrency %d", c.commandTimeout, c.concurrency)
	}
	if c.runner == nil {
		t.Error("no runner")
	}
	// Nonsensical options are ignored rather than producing a broken client.
	kept := New(WithSysfs(""), WithCommandTimeout(0), WithCommandTimeout(-time.Second), WithConcurrency(0), WithConcurrency(-4), WithRunner(nil))
	if kept.sysfs != DefaultSysfs || kept.commandTimeout != DefaultCommandTimeout || kept.concurrency != DefaultConcurrency || kept.runner == nil {
		t.Errorf("invalid options changed the client: %+v", kept)
	}
	tuned := New(WithSysfs("/tmp/enclosure"), WithCommandTimeout(time.Second), WithConcurrency(3))
	if tuned.sysfs != "/tmp/enclosure" || tuned.commandTimeout != time.Second || tuned.concurrency != 3 {
		t.Errorf("options not applied: %+v", tuned)
	}
}

// TestWithCopies covers C6: the exporter derives its own budgets, and the
// client the CLI holds must not change under the goroutines reading it.
func TestWithCopies(t *testing.T) {
	t.Parallel()
	calls := 0
	base := New(WithSysfs("/base"), WithRunner(func(context.Context, string, ...string) (string, error) {
		calls++
		return "", nil
	}))
	derived := base.With(WithSysfs("/derived"), WithConcurrency(1), WithCommandTimeout(time.Second))
	if base.sysfs != "/base" || base.concurrency != DefaultConcurrency || base.commandTimeout != DefaultCommandTimeout {
		t.Errorf("With mutated the receiver: %+v", base)
	}
	if derived.sysfs != "/derived" || derived.concurrency != 1 || derived.commandTimeout != time.Second {
		t.Errorf("With did not apply the options: %+v", derived)
	}
	// The copy keeps what it was not asked to change.
	if _, err := derived.exec(context.Background(), "lsscsi", "-g"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("derived client ran %d commands, want 1 through the inherited runner", calls)
	}
}
