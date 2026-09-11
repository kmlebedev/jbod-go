package jbod

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPreflightReportsEverythingMissing is the A4 contract: one message the
// operator can act on, instead of a missing binary surfacing in the middle
// of a collection.
func TestPreflightReportsEverythingMissing(t *testing.T) {
	t.Parallel()
	t.Run("missing sysfs", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "absent")
		c := New(WithSysfs(missing), WithRunner(func(context.Context, string, ...string) (string, error) {
			return "", nil
		}))
		err := c.Preflight()
		if err == nil {
			t.Fatal("an unreadable sysfs root passed preflight")
		}
		for _, want := range []string{missing, "enclosure driver"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})
	t.Run("empty sysfs", func(t *testing.T) {
		t.Parallel()
		c := New(WithSysfs(t.TempDir()), WithRunner(func(context.Context, string, ...string) (string, error) {
			return "", nil
		}))
		err := c.Preflight()
		if err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Fatalf("an empty sysfs root: %v", err)
		}
	})
	t.Run("usable", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "1:0:0:0"), 0o755); err != nil {
			t.Fatal(err)
		}
		c := New(WithSysfs(root), WithRunner(func(context.Context, string, ...string) (string, error) {
			return "", nil
		}))
		if err := c.Preflight(); err != nil {
			t.Fatalf("a usable client failed preflight: %v", err)
		}
	})
	t.Run("missing tools are listed with their packages", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "1:0:0:0"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A client with the real runner looks for the sg3-utils on this
		// host; the test only asserts the shape of the report when they are
		// absent, which is the case on any machine without sg3-utils.
		c := New(WithSysfs(root))
		err := c.Preflight()
		if err == nil {
			t.Skip("this host has all of lsscsi and the sg3-utils installed")
		}
		if !strings.Contains(err.Error(), "missing tools:") || !strings.Contains(err.Error(), "install:") {
			t.Fatalf("error %q does not name the tools and their packages", err)
		}
		// Every missing tool appears once, in one message.
		if n := strings.Count(err.Error(), "missing tools:"); n != 1 {
			t.Errorf("%d separate reports, want one", n)
		}
	})
}

// TestToolResolution covers A5: tools are looked up once, by absolute path.
func TestToolResolution(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	tools := newTools()
	// Something that exists in the fixed PATH on every unix.
	path, err := tools.path("sh")
	if err != nil {
		t.Fatalf("sh: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("path %q is not absolute", path)
	}
	again, err := tools.path("sh")
	if err != nil || again != path {
		t.Errorf("second lookup: %q %v", again, err)
	}
	if _, err := tools.path("definitely-not-a-real-tool"); err == nil {
		t.Error("a missing tool resolved")
	}
	// The miss is remembered, and reported the same way the second time.
	if _, err := tools.path("definitely-not-a-real-tool"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("second lookup of a missing tool: %v", err)
	}
}

// TestCommandEnvironment checks that commands run with the fixed environment
// rather than whatever the daemon inherited (A5).
func TestCommandEnvironment(t *testing.T) {
	// No t.Parallel: t.Setenv below rules it out.
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	t.Setenv("SECRET", "leaked")
	tools := newTools()
	out, err := tools.run(context.Background(), "sh", "-c", "echo $LC_ALL:$PATH:$SECRET")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(out)
	if !strings.HasPrefix(got, "C:") {
		t.Errorf("LC_ALL is not C: %q", got)
	}
	if !strings.Contains(got, commandPath) {
		t.Errorf("PATH is not the fixed one: %q", got)
	}
	if strings.Contains(got, "leaked") {
		t.Errorf("the inherited environment reached the command: %q", got)
	}
}
