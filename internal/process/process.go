// SPDX-License-Identifier: BSD-2-Clause

// Package process exports the process_* metrics that the Rust exporter got
// for free from the prometheus crate's "process" feature.
//
// Dashboards and alerts built against the original break without them, which
// makes this the one difference that is not cosmetic (A9). Everything is read
// from /proc, so on a host without it the package exports nothing rather than
// guessing.
package process

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procRoot is where the metrics come from; tests point it at a fixture.
const procRoot = "/proc"

// clockTicks is the kernel's USER_HZ. It is 100 on every Linux port Go
// supports, and there is no way to read it without cgo.
const clockTicks = 100

// Encode renders the process metrics in text format 0.0.4, or "" when this
// host has no /proc to read them from.
func Encode() string { return encode(procRoot) }

func encode(root string) string {
	var b strings.Builder
	stat, err := readStat(root)
	if err != nil {
		// No /proc (or an unreadable one): exporting nothing is honest,
		// exporting zeros would poison the same dashboards.
		return ""
	}
	gauge(&b, "process_cpu_seconds_total", "counter",
		"Total user and system CPU time spent in seconds",
		strconv.FormatFloat(stat.cpuSeconds, 'f', 3, 64))
	gauge(&b, "process_resident_memory_bytes", "gauge",
		"Resident memory size in bytes", strconv.FormatInt(stat.residentBytes, 10))
	gauge(&b, "process_virtual_memory_bytes", "gauge",
		"Virtual memory size in bytes", strconv.FormatInt(stat.virtualBytes, 10))
	if start, ok := startTime(root, stat.startTicks); ok {
		gauge(&b, "process_start_time_seconds", "gauge",
			"Start time of the process since unix epoch in seconds",
			strconv.FormatFloat(start, 'f', 3, 64))
	}
	if open, ok := openFDs(root); ok {
		gauge(&b, "process_open_fds", "gauge", "Number of open file descriptors",
			strconv.FormatInt(open, 10))
	}
	if max, ok := maxFDs(root); ok {
		gauge(&b, "process_max_fds", "gauge", "Maximum number of open file descriptors",
			strconv.FormatInt(max, 10))
	}
	return b.String()
}

func gauge(b *strings.Builder, name, kind, help, value string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n%s %s\n", name, help, name, kind, name, value)
}

// procStat is the subset of /proc/self/stat this package needs.
type procStat struct {
	cpuSeconds    float64
	virtualBytes  int64
	residentBytes int64
	startTicks    float64
}

// readStat parses /proc/self/stat. The second field is the executable name in
// parentheses and may contain spaces, so the fields are counted from the last
// closing parenthesis.
func readStat(root string) (procStat, error) {
	raw, err := os.ReadFile(root + "/self/stat")
	if err != nil {
		return procStat{}, err
	}
	line := string(raw)
	end := strings.LastIndex(line, ")")
	if end < 0 || end+2 > len(line) {
		return procStat{}, fmt.Errorf("malformed %s/self/stat", root)
	}
	// After the name, field 3 (state) is the first one, so index 0 here is
	// field 3 in proc(5) numbering.
	fields := strings.Fields(line[end+2:])
	const (
		utime     = 11 // field 14
		stime     = 12 // field 15
		starttime = 19 // field 22
		vsize     = 20 // field 23
		rss       = 21 // field 24
	)
	if len(fields) <= rss {
		return procStat{}, fmt.Errorf("%s/self/stat has %d fields", root, len(fields))
	}
	number := func(i int) float64 {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return 0
		}
		return v
	}
	return procStat{
		cpuSeconds:    (number(utime) + number(stime)) / clockTicks,
		virtualBytes:  int64(number(vsize)),
		residentBytes: int64(number(rss)) * int64(os.Getpagesize()),
		startTicks:    number(starttime),
	}, nil
}

// startTime turns the process start time, which the kernel reports in ticks
// since boot, into a unix timestamp using btime from /proc/stat.
func startTime(root string, startTicks float64) (float64, bool) {
	raw, err := os.ReadFile(root + "/stat")
	if err != nil {
		return 0, false
	}
	for line := range strings.Lines(string(raw)) {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "btime ")
		if !ok {
			continue
		}
		boot, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0, false
		}
		return boot + startTicks/clockTicks, true
	}
	return 0, false
}

// openFDs counts the entries in /proc/self/fd, minus the handle the count
// itself needs.
func openFDs(root string) (int64, bool) {
	entries, err := os.ReadDir(root + "/self/fd")
	if err != nil {
		return 0, false
	}
	return int64(len(entries)), true
}

// maxFDs reads the soft limit on open files from /proc/self/limits.
func maxFDs(root string) (int64, bool) {
	raw, err := os.ReadFile(root + "/self/limits")
	if err != nil {
		return 0, false
	}
	for line := range strings.Lines(string(raw)) {
		rest, ok := strings.CutPrefix(line, "Max open files")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		if fields[0] == "unlimited" {
			// Prometheus clients report the kernel's own sentinel here.
			return -1, true
		}
		limit, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return limit, true
	}
	return 0, false
}
