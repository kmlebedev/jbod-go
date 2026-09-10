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
	"slices"
	"strconv"
	"strings"
	"time"
)

type Runner func(context.Context, string, ...string) (string, error)

// Defaults for the knobs the exporter exposes as flags.
const (
	// DefaultCommandTimeout bounds a single external command.
	DefaultCommandTimeout = 15 * time.Second
	// DefaultConcurrency bounds external commands running at once. Twelve
	// keeps a 60-slot shelf inside a normal scrape interval without
	// flooding the SAS expander with queued SES requests.
	DefaultConcurrency = 12
)

type Client struct {
	Run   Runner
	Sysfs string
	// CommandTimeout bounds one external command; zero means
	// DefaultCommandTimeout.
	CommandTimeout time.Duration
	// Concurrency bounds simultaneously running external commands; zero
	// means DefaultConcurrency.
	Concurrency int
}

func New() *Client {
	return &Client{
		Run:            run,
		Sysfs:          "/sys/class/enclosure",
		CommandTimeout: DefaultCommandTimeout,
		Concurrency:    DefaultConcurrency,
	}
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// exec applies the per-command timeout. It lives here rather than in run so
// that an injected Runner is bounded by the same budget.
func (c *Client) exec(ctx context.Context, name string, args ...string) (string, error) {
	timeout := c.CommandTimeout
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.Run(ctx, name, args...)
}

func (c *Client) concurrency() int {
	if c.Concurrency > 0 {
		return c.Concurrency
	}
	return DefaultConcurrency
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

// Enclosures lists the enclosures. Failures on individual enclosures are
// reported as an error, but the enclosures that were read stay in the result.
func (c *Client) Enclosures(ctx context.Context) ([]Enclosure, error) {
	p := newProblems()
	result, err := c.enclosures(ctx, p)
	if err != nil {
		return nil, err
	}
	return result, p.err()
}

// enclosures discovers the shelves and fills in their identity. Only the
// lsscsi call is fatal: without it there is nothing to walk.
func (c *Client) enclosures(ctx context.Context, p *problems) ([]Enclosure, error) {
	out, err := c.exec(ctx, "lsscsi", "-g")
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
			p.fail(collectorEnclosures, fmt.Errorf("enclosure has no device: %s", line))
			continue
		}
		slot := strings.Trim(f[0], "[]")
		if slot == "" || strings.ContainsAny(slot, "/\\") || slot == "." || slot == ".." {
			p.fail(collectorEnclosures, fmt.Errorf("invalid enclosure slot %q", slot))
			continue
		}
		result = append(result, Enclosure{
			Slot:     slot,
			Device:   device,
			Vendor:   "NONE",
			Model:    "NONE",
			Revision: "NONE",
			Serial:   "NONE",
		})
	}
	// One sg_inq per shelf, in parallel: they are independent devices.
	forEach(ctx, c.concurrency(), len(result), func(i int) {
		details, err := c.exec(ctx, "sg_inq", result[i].Device)
		if err != nil {
			// The sentinels stay in place, so this is survivable for the
			// exporter, but the CLI would print a table of NONE.
			p.fail(collectorEnclosures, err)
			return
		}
		result[i].Vendor = field(details, "Vendor identification:", "NONE")
		result[i].Model = field(details, "Product identification:", "NONE")
		result[i].Revision = field(details, "Product revision level:", "NONE")
		result[i].Serial = field(details, "Unit serial number:", "NONE")
	})
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

// Disks lists the disks of the given enclosures, optionally with telemetry
// (vendor, model, serial, temperature, firmware). Enclosures whose sysfs tree
// cannot be read are reported as an error; the disks that were found stay in
// the result.
func (c *Client) Disks(ctx context.Context, enclosures []Enclosure, details bool) ([]Disk, error) {
	p := newProblems()
	ds := c.disks(ctx, enclosures, details, p)
	if err := p.err(); err != nil {
		return ds, err
	}
	return ds, ctx.Err()
}

func (c *Client) disks(ctx context.Context, enclosures []Enclosure, details bool, p *problems) []Disk {
	if len(enclosures) == 0 {
		return nil
	}
	mapping := map[string]string{}
	out, err := c.exec(ctx, "sg_map")
	if err != nil {
		// Without sg_map the /dev/sd* mapping is unknown, which only costs
		// the Map column; enumeration itself comes from sysfs.
		p.fail(collectorDisks, err)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 {
			mapping[f[0]] = f[1]
		}
	}
	var result []Disk
	// devices holds the sysfs device directory per result entry so the
	// parallel telemetry pass does not have to rebuild the path.
	var devices []string
	for _, enc := range enclosures {
		base := filepath.Join(c.Sysfs, enc.Slot)
		entries, err := os.ReadDir(base)
		if err != nil {
			// One unreadable shelf must not hide the other shelves (A6).
			p.fail(collectorDisks, fmt.Errorf("read enclosure sysfs: %w", err))
			continue
		}
		for _, entry := range entries {
			slotPath := filepath.Join(base, entry.Name())
			devPath := filepath.Join(slotPath, "device")
			generic, err := os.ReadDir(filepath.Join(devPath, "scsi_generic"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				p.fail(collectorDisks, err)
				continue
			}
			for _, g := range generic {
				d := Disk{
					Enclosure:   enc.Slot,
					Slot:        strings.SplitN(entry.Name(), ",", 2)[0],
					Device:      "/dev/" + g.Name(),
					Map:         "NONE",
					Temperature: "ERR",
					Firmware:    "N/A",
				}
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
				result = append(result, d)
				devices = append(devices, devPath)
			}
		}
	}
	if details {
		// Two external commands per disk: strictly sequential this is what
		// makes a 60-slot shelf miss its scrape deadline (B1).
		forEach(ctx, c.concurrency(), len(result), func(i int) {
			c.telemetry(ctx, &result[i], devices[i], p)
		})
	}
	slices.SortStableFunc(result, func(a, b Disk) int {
		if n := natCompare(a.Enclosure, b.Enclosure); n != 0 {
			return n
		}
		return natCompare(a.Slot, b.Slot)
	})
	return result
}

// telemetry fills in the optional per-disk fields. Every failure keeps the
// sentinel that is already in place, so a disk with a dead sensor is still
// listed.
func (c *Client) telemetry(ctx context.Context, d *Disk, devPath string, p *problems) {
	d.Vendor = readText(filepath.Join(devPath, "vendor"))
	d.Model = readText(filepath.Join(devPath, "model"))
	d.Serial = serial(filepath.Join(devPath, "vpd_pg80"))
	if out, err := c.exec(ctx, "scsi_temperature", d.Device); err != nil {
		p.note(collectorDisks, err)
	} else {
		d.Temperature = temperature(out)
	}
	if out, err := c.exec(ctx, "sginfo", d.Device); err != nil {
		p.note(collectorDisks, err)
	} else {
		d.Firmware = field(out, "Revision level:", "N/A")
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// natCompare orders strings so that embedded decimal runs compare by value:
// "Slot 2" sorts before "Slot 10", and "1:0:0:0" before "10:0:0:0".
// Strings that differ only in leading zeros fall back to byte order so the
// result stays a strict weak ordering.
func natCompare(a, b string) int {
	x, y := a, b
	for x != "" && y != "" {
		xd, yd := isDigit(x[0]), isDigit(y[0])
		if xd != yd {
			return strings.Compare(x, y)
		}
		i, j := 0, 0
		for i < len(x) && isDigit(x[i]) == xd {
			i++
		}
		for j < len(y) && isDigit(y[j]) == yd {
			j++
		}
		if xd {
			xn := strings.TrimLeft(x[:i], "0")
			yn := strings.TrimLeft(y[:j], "0")
			if len(xn) != len(yn) {
				return len(xn) - len(yn)
			}
			if c := strings.Compare(xn, yn); c != 0 {
				return c
			}
		} else if c := strings.Compare(x[:i], y[:j]); c != 0 {
			return c
		}
		x, y = x[i:], y[j:]
	}
	if c := strings.Compare(x, y); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

var fanLine = regexp.MustCompile(`(.*?)\[(-?\d+,-?\d+)\].*Cooling`)
var rpm = regexp.MustCompile(`(?i)(\d+)\s*rpm`)

// fan is a cooling element found in the element listing, before its speed is
// known.
type fan struct {
	enc         Enclosure
	description string
	index       string
}

// Fans lists the cooling elements of the given enclosures with their speed.
// A fan whose speed cannot be read is skipped rather than failing the whole
// listing; a shelf whose element listing fails is reported as an error.
func (c *Client) Fans(ctx context.Context, enclosures []Enclosure) ([]Fan, error) {
	p := newProblems()
	fs := c.fans(ctx, enclosures, p)
	if err := p.err(); err != nil {
		return fs, err
	}
	return fs, ctx.Err()
}

func (c *Client) fans(ctx context.Context, enclosures []Enclosure, p *problems) []Fan {
	// Pass 1: element listing per shelf, in parallel.
	lists := make([][]fan, len(enclosures))
	forEach(ctx, c.concurrency(), len(enclosures), func(i int) {
		out, err := c.exec(ctx, "sg_ses", "-j", "-ff", enclosures[i].Device)
		if err != nil {
			p.fail(collectorFans, err)
			return
		}
		for _, line := range strings.Split(out, "\n") {
			m := fanLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			lists[i] = append(lists[i], fan{enc: enclosures[i], description: strings.TrimSpace(m[1]), index: m[2]})
		}
	})
	// Deduplicate in enclosure order so the result does not depend on which
	// goroutine finished first.
	var found []fan
	seen := map[string]bool{}
	for _, list := range lists {
		for _, f := range list {
			key := f.enc.Serial + "\x00" + f.index
			if f.enc.Serial == "NONE" {
				key = f.enc.Slot + "\x00" + f.index
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			found = append(found, f)
		}
	}
	// Pass 2: one sg_ses per element, in parallel; results are written by
	// index so the order above is preserved.
	result := make([]Fan, len(found))
	ok := make([]bool, len(found))
	forEach(ctx, c.concurrency(), len(found), func(i int) {
		f := found[i]
		out, err := c.exec(ctx, "sg_ses", "--index="+f.index, f.enc.Device)
		if err != nil {
			p.note(collectorFans, err)
			return
		}
		match := rpm.FindStringSubmatch(out)
		if match == nil {
			// A single sensor without an RPM line used to fail the scrape
			// and take the temperatures down with it (A6).
			p.note(collectorFans, fmt.Errorf("no fan RPM for %s index %s", f.enc.Device, f.index))
			return
		}
		speed, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			p.note(collectorFans, err)
			return
		}
		comment := ""
		for _, l := range strings.Split(out, "\n") {
			if rpm.MatchString(l) {
				parts := strings.SplitN(l, ",", 3)
				if len(parts) == 3 {
					comment = strings.TrimSpace(parts[2])
				}
			}
		}
		result[i] = Fan{
			Slot:        f.enc.Slot,
			Serial:      f.enc.Serial,
			Description: f.description,
			Index:       f.index,
			Comment:     comment,
			Speed:       speed,
		}
		ok[i] = true
	})
	// Drop the elements whose speed could not be read.
	fans := result[:0]
	for i, f := range result {
		if ok[i] {
			fans = append(fans, f)
		}
	}
	if len(fans) == 0 {
		return nil
	}
	return fans
}

// SetLED opens only an existing sysfs attribute; it never creates a file.
func SetLED(disks []Disk, device, kind string, on bool) error {
	if kind != "locate" && kind != "fault" {
		return fmt.Errorf("unknown LED kind %q", kind)
	}
	for _, d := range disks {
		// device is validated as a /dev/ path by the caller, so it can never
		// equal the "NONE" sentinel and a plain comparison is enough.
		if d.Device != device && d.Map != device {
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
