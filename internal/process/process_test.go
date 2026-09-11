package process

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeProc writes a /proc tree with the fields the encoder reads. The comm
// field deliberately contains spaces and a parenthesis, which is what breaks
// naive field splitting of /proc/self/stat.
func fakeProc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	self := filepath.Join(root, "self")
	if err := os.MkdirAll(filepath.Join(self, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		if err := os.WriteFile(filepath.Join(self, "fd", fmt.Sprint(i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Fields 1..24 of proc(5): utime=150, stime=50, starttime=1000,
	// vsize=2097152, rss=512 pages.
	fields := make([]string, 0, 24)
	fields = append(fields, "42", "(jbod (exporter))", "S")
	for i := 4; i <= 24; i++ {
		switch i {
		case 14:
			fields = append(fields, "150")
		case 15:
			fields = append(fields, "50")
		case 22:
			fields = append(fields, "1000")
		case 23:
			fields = append(fields, "2097152")
		case 24:
			fields = append(fields, "512")
		default:
			fields = append(fields, "0")
		}
	}
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(self, "stat"), strings.Join(fields, " ")+"\n")
	write(filepath.Join(self, "limits"), strings.Join([]string{
		"Limit                     Soft Limit           Hard Limit           Units",
		"Max cpu time              unlimited            unlimited            seconds",
		"Max open files            1024                 4096                 files",
		"",
	}, "\n"))
	write(filepath.Join(root, "stat"), "cpu  1 2 3\nbtime 1700000000\nprocesses 12\n")
	return root
}

func TestEncode(t *testing.T) {
	t.Parallel()
	got := encode(fakeProc(t))
	page := int64(os.Getpagesize())
	want := []string{
		"# TYPE process_cpu_seconds_total counter\nprocess_cpu_seconds_total 2.000\n",
		fmt.Sprintf("process_resident_memory_bytes %d\n", 512*page),
		"process_virtual_memory_bytes 2097152\n",
		// btime 1700000000 plus 1000 ticks at 100 Hz.
		"process_start_time_seconds 1700000010.000\n",
		"process_open_fds 4\n",
		"process_max_fds 1024\n",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	// Every series carries HELP and TYPE, or Prometheus warns about it.
	for _, name := range []string{
		"process_cpu_seconds_total", "process_resident_memory_bytes",
		"process_virtual_memory_bytes", "process_start_time_seconds",
		"process_open_fds", "process_max_fds",
	} {
		if !strings.Contains(got, "# HELP "+name+" ") || !strings.Contains(got, "# TYPE "+name+" ") {
			t.Errorf("%s has no HELP/TYPE:\n%s", name, got)
		}
	}
}

// TestEncodeWithoutProc keeps the exporter honest on a host that has no
// /proc: no metrics at all rather than zeros.
func TestEncodeWithoutProc(t *testing.T) {
	t.Parallel()
	if got := encode(filepath.Join(t.TempDir(), "absent")); got != "" {
		t.Errorf("expected no output, got:\n%s", got)
	}
	// A truncated stat file is the same case.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "self", "stat"), []byte("42 (jbod) S 1 2 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := encode(root); got != "" {
		t.Errorf("expected no output for a short stat file, got:\n%s", got)
	}
}

// TestEncodeUnlimitedFDs covers the sentinel the kernel prints for an
// unlimited soft limit.
func TestEncodeUnlimitedFDs(t *testing.T) {
	t.Parallel()
	root := fakeProc(t)
	if err := os.WriteFile(filepath.Join(root, "self", "limits"),
		[]byte("Max open files            unlimited            unlimited            files\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := encode(root); !strings.Contains(got, "process_max_fds -1\n") {
		t.Errorf("unlimited soft limit:\n%s", got)
	}
}

// TestEncodeOnThisHost runs against the real /proc where there is one, so the
// parser is known to match the kernel and not just the fixture.
func TestEncodeOnThisHost(t *testing.T) {
	t.Parallel()
	got := Encode()
	if runtime.GOOS != "linux" {
		if got != "" {
			t.Errorf("%s has no /proc but produced:\n%s", runtime.GOOS, got)
		}
		t.Skipf("no /proc on %s", runtime.GOOS)
	}
	for _, name := range []string{
		"process_cpu_seconds_total", "process_resident_memory_bytes",
		"process_virtual_memory_bytes", "process_start_time_seconds",
		"process_open_fds", "process_max_fds",
	} {
		if !strings.Contains(got, "\n"+name+" ") && !strings.HasPrefix(got, name+" ") {
			t.Errorf("%s is missing from the real /proc output:\n%s", name, got)
		}
	}
}
