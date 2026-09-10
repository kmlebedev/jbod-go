package cli

import (
	"strings"
	"testing"
)

// TestResolveVersion covers the order of preference behind Version: the
// -ldflags stamp first, then whatever the build recorded, and never an empty
// string (F).
func TestResolveVersion(t *testing.T) {
	t.Parallel()
	if got := resolveVersion("1.2.3"); got != "1.2.3" {
		t.Errorf("stamped build reports %q", got)
	}
	unstamped := resolveVersion("")
	if unstamped == "" {
		t.Error("an unstamped build reports no version at all")
	}
	if strings.ContainsAny(unstamped, " \t\n") {
		t.Errorf("version %q is not a single token", unstamped)
	}
	if got := Version(); got == "" {
		t.Error("Version() is empty")
	}
}
