package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// build compiles the binary under test once and returns its path. Running the
// real binary is the only way to cover main, the signal wiring and the exit
// codes; everything else is covered in internal/cli.
func build(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a binary")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	binary := filepath.Join(t.TempDir(), "jbod")
	out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return binary
}

func TestBinary(t *testing.T) {
	t.Parallel()
	binary := build(t)
	cases := []struct {
		name     string
		args     []string
		exitCode int
		stdout   string
		stderr   string
	}{
		{name: "no arguments", stdout: "jbod - Generic storage enclosure tool"},
		{name: "help", args: []string{"help"}, stdout: "jbod list"},
		{name: "version", args: []string{"--version"}, stdout: "jbod-go "},
		{
			// The usage of a subcommand comes from pflag and is not a
			// failure for the shell.
			name: "subcommand help", args: []string{"list", "--help"},
			stdout: "--enclosure",
		},
		{
			name: "unknown command", args: []string{"nonsense"},
			exitCode: 1, stderr: `jbod: unknown command "nonsense"`,
		},
		{
			name: "nothing selected", args: []string{"list"},
			exitCode: 1, stderr: "list requires",
		},
		{
			name: "bad flag", args: []string{"list", "-z"},
			exitCode: 1, stderr: "unknown shorthand",
		},
		{
			name: "led without a state", args: []string{"led", "-l", "/dev/sda"},
			exitCode: 1, stderr: "exactly one",
		},
		{
			name: "bad port", args: []string{"prometheus", "--port", "70000"},
			exitCode: 1, stderr: `invalid port "70000"`,
		},
		{
			// The v1.1 commands exist and reach the hardware check, which
			// on a machine with no enclosure is where they stop.
			name: "capabilities without hardware", args: []string{"capabilities"},
			exitCode: 1, stderr: "enclosure",
		},
		{
			name: "slots without hardware", args: []string{"list", "--slots"},
			exitCode: 1, stderr: "enclosure",
		},
		{
			name: "led target that is neither a device nor a slot", args: []string{"led", "-l", "sda", "--on"},
			exitCode: 1, stderr: "/dev/sda",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(binary, c.args...)
			// A clean environment: the binary must not depend on one.
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			var stdout, stderr strings.Builder
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			code := 0
			if err := cmd.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("%v (stderr: %s)", err, stderr.String())
				}
				code = exit.ExitCode()
			}
			if code != c.exitCode {
				t.Errorf("exit code %d, want %d (stdout: %s, stderr: %s)", code, c.exitCode, stdout.String(), stderr.String())
			}
			if c.stdout != "" && !strings.Contains(stdout.String(), c.stdout) {
				t.Errorf("stdout %q does not contain %q", stdout.String(), c.stdout)
			}
			if c.stderr != "" && !strings.Contains(stderr.String(), c.stderr) {
				t.Errorf("stderr %q does not contain %q", stderr.String(), c.stderr)
			}
			// Diagnostics never land on the stream that carries data.
			if c.exitCode != 0 && strings.Contains(stdout.String(), "jbod:") {
				t.Errorf("the error was written to stdout: %s", stdout.String())
			}
		})
	}
}

// TestExporterBinary checks the second entry point: the same body, a
// different command.
func TestExporterBinary(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds a binary")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	binary := filepath.Join(t.TempDir(), "prometheus-jbod-exporter")
	out, err := exec.Command("go", "build", "-o", binary, "../prometheus-jbod-exporter").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	version, err := exec.Command(binary, "--version").Output()
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.HasPrefix(string(version), "jbod-go ") {
		t.Errorf("--version printed %q", version)
	}
	help, err := exec.Command(binary, "--help").Output()
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	if !strings.Contains(string(help), "prometheus-jbod-exporter") {
		t.Errorf("--help printed %q", help)
	}
	// An unusable host is reported as a failure to start, with the reason,
	// rather than a daemon that answers every scrape with a 503 (A4). On a
	// host that can actually collect there is nothing to assert.
	cmd := exec.Command(binary, "--port", "0")
	cmd.Env = append(os.Environ(), "PATH=/nonexistent")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an invalid port was accepted")
		}
		if !strings.Contains(stderr.String(), "exporter:") {
			t.Errorf("stderr %q does not name the binary", stderr.String())
		}
		// Either the preflight check or the port validation refused; both
		// name what is wrong.
		if !strings.Contains(stderr.String(), "invalid port") && !strings.Contains(stderr.String(), "missing tools") &&
			!strings.Contains(stderr.String(), "/sys/class/enclosure") {
			t.Errorf("stderr %q explains nothing", stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the exporter neither started nor refused")
	}
}
