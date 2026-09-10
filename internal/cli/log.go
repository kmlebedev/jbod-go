// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Log formats. The daemon defaults to JSON so journald indexes the fields;
// a person running a command gets plain text.
const (
	LogFormatJSON = "json"
	LogFormatText = "text"
)

// newLogger builds a logger writing to w. Logs go to stderr, never to the
// stream that carries the tables or the metrics.
func newLogger(w io.Writer, format string, level slog.Level) (*slog.Logger, error) {
	opts := &slog.HandlerOptions{Level: level}
	switch strings.ToLower(format) {
	case LogFormatJSON:
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	case LogFormatText:
		return slog.New(slog.NewTextHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("invalid log format %q; use %s or %s", format, LogFormatJSON, LogFormatText)
	}
}

// parseLevel accepts the slog spellings: debug, info, warn, error, and
// offsets such as "warn+2".
func parseLevel(s string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("invalid log level %q; use debug, info, warn or error", s)
	}
	return level, nil
}
