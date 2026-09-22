// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// commands are the external tools this package needs, with the Debian
// package each one comes from, so a missing tool can be reported as
// something the operator can install (A4).
var commands = []struct {
	Name    string
	Package string
}{
	{"lsscsi", "lsscsi"},
	{"sg_inq", "sg3-utils"},
	{"sg_map", "sg3-utils"},
	{"sg_ses", "sg3-utils"},
	{"sginfo", "sg3-utils"},
	{"scsi_temperature", "sg3-utils"},
}

// optionalCommands are the tools only one capability needs, so Preflight
// must not look for them: a machine with no expander has no use for
// smp_utils and is not misconfigured for lacking it (ROADMAP 3).
//
// They are listed here for one reason: the "not found" message should name
// the package either way. On a shelf whose SMP half is missing, "install
// the smp-utils package" is the whole answer, and an operator should not
// have to look it up.
var optionalCommands = []struct {
	Name    string
	Package string
}{
	{smpReportGeneral, "smp-utils"},
	{smpDiscover, "smp-utils"},
	{smpPhyErrorLog, "smp-utils"},
}

// toolPackage names the package a command comes from, for the message
// below.
func toolPackage(name string) string {
	for _, list := range [][]struct {
		Name    string
		Package string
	}{commands, optionalCommands} {
		for _, command := range list {
			if command.Name == name {
				return command.Package
			}
		}
	}
	return ""
}

// notFound is the one spelling of "this tool is not installed".
//
// It is a function rather than two fmt.Errorf calls because the lookup is
// cached: the first miss and every later one used to produce differently
// worded errors for the same condition, and a report that listed six
// expanders showed both spellings of it.
func notFound(name string) error {
	if pkg := toolPackage(name); pkg != "" {
		return fmt.Errorf("%s: not found in %s or PATH (install the %s package)", name, commandPath, pkg)
	}
	return fmt.Errorf("%s: not found in %s or PATH", name, commandPath)
}

// commandPath is the PATH the external tools are looked up in and run with.
//
// The process is normally root and started by systemd, so it must not depend
// on an inherited PATH; scsi_temperature is a shell script that calls
// sg_logs, so its own PATH has to be usable too (A5).
const commandPath = "/usr/sbin:/usr/bin:/sbin:/bin"

// commandEnv is the whole environment external commands get. LC_ALL=C is not
// cosmetic: the parsers expect the English output of sg3-utils.
var commandEnv = []string{"PATH=" + commandPath, "LC_ALL=C", "LANG=C"}

// tools resolves command names to absolute paths, once per name, and runs
// them with a fixed environment.
type tools struct {
	mu    sync.Mutex
	paths map[string]string
}

func newTools() *tools { return &tools{paths: map[string]string{}} }

// path returns the absolute path of name, looking it up at most once.
func (t *tools) path(name string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if path, ok := t.paths[name]; ok {
		if path == "" {
			return "", notFound(name)
		}
		return path, nil
	}
	path, err := lookPath(name)
	if err != nil {
		// Remember the miss too: a scrape asks for the same tool once per
		// disk, and a missing binary will not appear mid-scrape.
		t.paths[name] = ""
		return "", err
	}
	t.paths[name] = path
	return path, nil
}

// run executes name with a clean environment and returns its stdout.
func (t *tools) run(ctx context.Context, name string, args ...string) (string, error) {
	path, err := t.path(name)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = commandEnv
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// lookPath finds an executable in commandPath first, so a root daemon does
// not pick up something earlier in an inherited PATH, and falls back to the
// process PATH for installations that put the tools elsewhere.
func lookPath(name string) (string, error) {
	for dir := range strings.SplitSeq(commandPath, ":") {
		candidate := dir + "/" + name
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", notFound(name)
	}
	return path, nil
}

// Preflight checks that the tools and the sysfs tree the client needs are
// there, and reports everything that is missing at once.
//
// Both binaries call it before doing any work: the alternative is an
// operator meeting `exec: "sg_inq": executable file not found in $PATH` in
// the middle of a collection, or a 503 with no explanation (A4).
func (c *Client) Preflight() error {
	var problems []error
	var missing []string
	packages := map[string]bool{}
	// With an injected runner there are no binaries to find: only the
	// sysfs tree below is worth checking.
	if c.tools != nil {
		for _, command := range commands {
			if _, err := c.tools.path(command.Name); err != nil {
				missing = append(missing, command.Name)
				packages[command.Package] = true
			}
		}
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(packages))
		for name := range packages {
			names = append(names, name)
		}
		slices.Sort(names)
		problems = append(problems, fmt.Errorf("missing tools: %s (install: %s)",
			strings.Join(missing, ", "), strings.Join(names, " ")))
	}
	entries, err := os.ReadDir(c.sysfs)
	switch {
	case err != nil:
		problems = append(problems, fmt.Errorf("%s is not readable: %w; is the enclosure driver loaded (modprobe enclosure) and is this a Linux host with SES?", c.sysfs, err))
	case len(entries) == 0:
		problems = append(problems, fmt.Errorf("%s is empty: no enclosure exposes slots; check that the HBA is in non-RAID mode and that the shelf reports SES", c.sysfs))
	}
	// The failure is returned, not logged: both callers report it, and
	// logging it here would print the same thing twice.
	if err := errors.Join(problems...); err != nil {
		return err
	}
	c.logger.Debug("preflight passed", "sysfs", c.sysfs, "enclosures", len(entries))
	return nil
}
