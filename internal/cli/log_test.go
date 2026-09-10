package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLogger(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	json, err := newLogger(&buf, LogFormatJSON, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	json.Info("started", "address", "127.0.0.1:9945")
	json.Debug("quiet", "hidden", true)
	got := buf.String()
	if !strings.Contains(got, `"msg":"started"`) || !strings.Contains(got, `"address":"127.0.0.1:9945"`) {
		t.Errorf("json output: %q", got)
	}
	if strings.Contains(got, "quiet") {
		t.Errorf("a record below the level was emitted: %q", got)
	}

	buf.Reset()
	text, err := newLogger(&buf, "TEXT", slog.LevelWarn)
	if err != nil {
		t.Fatal(err)
	}
	text.Warn("collection error", "collector", "fans")
	if got := buf.String(); !strings.Contains(got, `msg="collection error"`) || !strings.Contains(got, "collector=fans") {
		t.Errorf("text output: %q", got)
	}

	if _, err := newLogger(&buf, "yaml", slog.LevelInfo); err == nil {
		t.Error("accepted an unknown log format")
	}
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"INFO":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	for in, want := range cases {
		got, err := parseLevel(in)
		if err != nil || got != want {
			t.Errorf("parseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseLevel("chatty"); err == nil {
		t.Error("accepted an unknown level")
	}
}
