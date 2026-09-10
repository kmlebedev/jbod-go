// SPDX-License-Identifier: BSD-2-Clause
package jbod

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func label(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(s)
}

// Metrics collects a fresh snapshot so removed hardware does not leave stale series.
func (c *Client) Metrics(ctx context.Context) (string, error) {
	enc, err := c.Enclosures(ctx)
	if err != nil {
		return "", err
	}
	disks, err := c.Disks(ctx, enc, true)
	if err != nil {
		return "", err
	}
	fans, err := c.Fans(ctx, enc)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP number_of_enclosures Number of enclosures\n# TYPE number_of_enclosures gauge\nnumber_of_enclosures %d\n", len(enc))
	b.WriteString("# HELP jbod_slot_temperature Enclosure number, slot position and temperature\n# TYPE jbod_slot_temperature gauge\n")
	// Match the original gauge-vector behavior: the last value wins for duplicate labels.
	temps := map[string]int64{}
	var tempKeys []string
	for _, d := range disks {
		n, e := strconv.ParseInt(d.Temperature, 10, 64)
		if e != nil {
			continue
		}
		key := fmt.Sprintf("slot=\"%s\",enclosure=\"%s\"", label(d.Slot), label(d.Enclosure))
		if _, ok := temps[key]; !ok {
			tempKeys = append(tempKeys, key)
		}
		temps[key] = n
	}
	for _, key := range tempKeys {
		fmt.Fprintf(&b, "jbod_slot_temperature{%s} %d\n", key, temps[key])
	}
	b.WriteString("# HELP jbod_fan_rpm The RPM speed of FAN components, device and slot\n# TYPE jbod_fan_rpm gauge\n")
	speeds := map[string]int64{}
	var fanKeys []string
	for _, f := range fans {
		key := fmt.Sprintf("device=\"%s\",slot=\"%s\"", label(f.Description), label(f.Index))
		if _, ok := speeds[key]; !ok {
			fanKeys = append(fanKeys, key)
		}
		speeds[key] = f.Speed
	}
	for _, key := range fanKeys {
		fmt.Fprintf(&b, "jbod_fan_rpm{%s} %d\n", key, speeds[key])
	}
	return b.String(), nil
}

func (c *Client) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", 405)
			return
		}
		if r.URL.Path == "/" {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		result, err := c.Metrics(ctx)
		if err != nil {
			http.Error(w, "collection failed: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if r.Method != http.MethodHead {
			fmt.Fprint(w, result)
		}
	})
}
