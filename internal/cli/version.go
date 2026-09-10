// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"cmp"
	"runtime/debug"
	"sync"
)

// version is stamped by the build:
//
//	go build -ldflags "-X github.com/kmlebedev/jbod-go/internal/cli.version=1.2.3"
//
// The Makefile passes git describe. It used to be a constant that had to be
// kept in sync with debian/control by hand (F).
var version string

// Version reports the version of the running binary: the stamp if the build
// set one, otherwise what the module recorded in the binary, otherwise "dev".
var Version = sync.OnceValue(func() string { return resolveVersion(version) })

// resolveVersion is the order of preference, split out so it can be tested
// without a stamped build.
func resolveVersion(stamp string) string {
	return cmp.Or(stamp, buildVersion(), "dev")
}

// buildVersion reads the version out of the binary itself, which covers
// "go install pkg@v1.2.3" (a module version) and a plain "go build" in a
// checkout (the VCS revision).
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision == "" {
		return ""
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if modified == "true" {
		return revision + "-dirty"
	}
	return revision
}
