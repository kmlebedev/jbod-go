// SPDX-License-Identifier: BSD-2-Clause
// Copyright (c) 2021-2023, Gandi S.A.S.
// Go port of Gandi/jbod-rs.
package jbod

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Runner func(context.Context, string, ...string) (string, error)

type Client struct {
	Run   Runner
	Sysfs string
}

func New() *Client { return &Client{Run: run, Sysfs: "/sys/class/enclosure"} }

func run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

type Enclosure struct{ Slot, Device, Vendor, Model, Revision, Serial string }
type Disk struct{ Enclosure, Slot, Device, Map, Temperature, Vendor, Model, Serial, Firmware, Locate, Fault string }
type Fan struct {
	Slot, Serial, Description, Index, Comment string
	Speed                                     int64
}

func field(s, key, fallback string) string {
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key); ok {
			return strings.TrimSpace(v)
		}
	}
	return fallback
}

func (c *Client) Enclosures(ctx context.Context) ([]Enclosure, error) {
	out, err := c.Run(ctx, "lsscsi", "-g")
	if err != nil {
		return nil, err
	}
	var result []Enclosure
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[1], "enclosu") {
			continue
		}
		device := ""
		for _, v := range f[2:] {
			if strings.HasPrefix(v, "/dev/") {
				device = v
				break
			}
		}
		if device == "" {
			return nil, fmt.Errorf("enclosure has no device: %s", line)
		}
		slot := strings.Trim(f[0], "[]")
		if slot == "" || strings.ContainsAny(slot, "/\\") || slot == "." || slot == ".." {
			return nil, fmt.Errorf("invalid enclosure slot %q", slot)
		}
		details, err := c.Run(ctx, "sg_inq", device)
		if err != nil {
			return nil, err
		}
		result = append(result, Enclosure{slot, device, field(details, "Vendor identification:", "NONE"), field(details, "Product identification:", "NONE"), field(details, "Product revision level:", "NONE"), field(details, "Unit serial number:", "NONE")})
	}
	return result, nil
}

func readText(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "N/A"
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(b), "�"))
}

func serial(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "N/A"
	}
	if len(b) < 4 || b[1] != 0x80 {
		return "N/A"
	}
	n := int(binary.BigEndian.Uint16(b[2:4]))
	if n > len(b)-4 {
		return "N/A"
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(b[4:4+n]), "�"))
}

var number = regexp.MustCompile(`-?\d+`)

func temperature(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(strings.ToLower(line), "current") && strings.Contains(strings.ToLower(line), "temperature") {
			_, value, ok := strings.Cut(line, ":")
			if ok {
				if n := number.FindString(value); n != "" {
					return n
				}
			}
		}
	}
	return "ERR"
}

func (c *Client) Disks(ctx context.Context, enclosures []Enclosure, details bool) ([]Disk, error) {
	if len(enclosures) == 0 {
		return nil, nil
	}
	out, err := c.Run(ctx, "sg_map")
	if err != nil {
		return nil, err
	}
	mapping := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 {
			mapping[f[0]] = f[1]
		}
	}
	var result []Disk
	for _, enc := range enclosures {
		base := filepath.Join(c.Sysfs, enc.Slot)
		entries, err := os.ReadDir(base)
		if err != nil {
			return nil, fmt.Errorf("read enclosure sysfs: %w", err)
		}
		for _, entry := range entries {
			slotPath := filepath.Join(base, entry.Name())
			devPath := filepath.Join(slotPath, "device")
			generic, err := os.ReadDir(filepath.Join(devPath, "scsi_generic"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, g := range generic {
				d := Disk{Enclosure: enc.Slot, Slot: strings.SplitN(entry.Name(), ",", 2)[0], Device: "/dev/" + g.Name(), Map: "NONE", Temperature: "ERR", Firmware: "N/A"}
				if m := mapping[d.Device]; m != "" {
					d.Map = m
				}
				for _, kind := range []string{"locate", "fault"} {
					path := filepath.Join(slotPath, kind)
					if _, err := os.Stat(path); err == nil {
						if kind == "locate" {
							d.Locate = path
						} else {
							d.Fault = path
						}
					}
				}
				if details {
					d.Vendor = readText(filepath.Join(devPath, "vendor"))
					d.Model = readText(filepath.Join(devPath, "model"))
					d.Serial = serial(filepath.Join(devPath, "vpd_pg80"))
					temp, e := c.Run(ctx, "scsi_temperature", d.Device)
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					if e == nil {
						d.Temperature = temperature(temp)
					}
					fw, e := c.Run(ctx, "sginfo", d.Device)
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					if e == nil {
						d.Firmware = field(fw, "Revision level:", "N/A")
					}
				}
				result = append(result, d)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Enclosure != result[j].Enclosure {
			return result[i].Enclosure < result[j].Enclosure
		}
		return result[i].Slot < result[j].Slot
	})
	return result, nil
}

var fanLine = regexp.MustCompile(`(.*?)\[(-?\d+,-?\d+)\].*Cooling`)
var rpm = regexp.MustCompile(`(?i)(\d+)\s*rpm`)

func (c *Client) Fans(ctx context.Context, enclosures []Enclosure) ([]Fan, error) {
	var result []Fan
	seen := map[string]bool{}
	for _, enc := range enclosures {
		out, err := c.Run(ctx, "sg_ses", "-j", "-ff", enc.Device)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			m := fanLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			key := enc.Serial + "\x00" + m[2]
			if enc.Serial == "NONE" {
				key = enc.Slot + "\x00" + m[2]
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			speed, err := c.Run(ctx, "sg_ses", "--index="+m[2], enc.Device)
			if err != nil {
				return nil, err
			}
			match := rpm.FindStringSubmatch(speed)
			if match == nil {
				return nil, fmt.Errorf("no fan RPM for %s index %s", enc.Device, m[2])
			}
			n, err := strconv.ParseInt(match[1], 10, 64)
			if err != nil {
				return nil, err
			}
			comment := ""
			for _, l := range strings.Split(speed, "\n") {
				if rpm.MatchString(l) {
					parts := strings.SplitN(l, ",", 3)
					if len(parts) == 3 {
						comment = strings.TrimSpace(parts[2])
					}
				}
			}
			result = append(result, Fan{enc.Slot, enc.Serial, strings.TrimSpace(m[1]), m[2], comment, n})
		}
	}
	return result, nil
}

// SetLED opens only an existing sysfs attribute; it never creates a file.
func SetLED(disks []Disk, device, kind string, on bool) error {
	if kind != "locate" && kind != "fault" {
		return fmt.Errorf("unknown LED kind %q", kind)
	}
	for _, d := range disks {
		if d.Device != device && (d.Map == "NONE" || d.Map != device) {
			continue
		}
		path := d.Locate
		if kind == "fault" {
			path = d.Fault
		}
		if path == "" {
			return fmt.Errorf("%s does not expose %s LED", device, kind)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			return err
		}
		value := "0"
		if on {
			value = "1"
		}
		_, err = f.WriteString(value)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	return fmt.Errorf("device %s not found in enclosures", device)
}
