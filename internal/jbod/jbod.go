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

// Runner executes an external command and returns its stdout. Tests replace
// it with WithRunner.
type Runner func(context.Context, string, ...string) (string, error)

// Defaults for the knobs the exporter exposes as flags.
const (
	// DefaultSysfs is where the enclosure driver exposes its slots.
	DefaultSysfs = "/sys/class/enclosure"
	// DefaultCommandTimeout bounds a single external command.
	DefaultCommandTimeout = 15 * time.Second
	// DefaultConcurrency bounds external commands running at once. Twelve
	// keeps a 60-slot shelf inside a normal scrape interval without
	// flooding the SAS expander with queued SES requests.
	DefaultConcurrency = 12
)

// Client reads storage enclosures through sysfs and the sg3-utils.
//
// Its fields are private and never change after New: an HTTP handler reads
// the client from several goroutines at once, so a mutable, publicly writable
// client was a data race waiting for a test to reassign it (C6). Derive a
// differently configured client with With.
type Client struct {
	runner         Runner
	sysfs          string
	commandTimeout time.Duration
	concurrency    int
}

// Option configures a Client. Values that make no sense (an empty sysfs
// path, a non-positive timeout) are ignored, so New always returns a usable
// client and an empty Sysfs cannot be a working state (C7).
type Option func(*Client)

// WithRunner replaces the external command execution; for tests.
func WithRunner(r Runner) Option {
	return func(c *Client) {
		if r != nil {
			c.runner = r
		}
	}
}

// WithSysfs sets the enclosure sysfs root.
func WithSysfs(path string) Option {
	return func(c *Client) {
		if path != "" {
			c.sysfs = path
		}
	}
}

// WithCommandTimeout bounds one external command.
func WithCommandTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.commandTimeout = d
		}
	}
}

// WithConcurrency bounds external commands running at once.
func WithConcurrency(n int) Option {
	return func(c *Client) {
		if n > 0 {
			c.concurrency = n
		}
	}
}

// New returns a client for the local hardware, with opts applied. It is the
// only way to build one.
func New(opts ...Option) *Client {
	c := &Client{
		runner:         run,
		sysfs:          DefaultSysfs,
		commandTimeout: DefaultCommandTimeout,
		concurrency:    DefaultConcurrency,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// With returns a copy of c with opts applied. The receiver is untouched, so
// the CLI can hand the exporter a client with its own budgets without two
// goroutines sharing mutable state.
func (c *Client) With(opts ...Option) *Client {
	clone := *c
	for _, opt := range opts {
		opt(&clone)
	}
	return &clone
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
	ctx, cancel := context.WithTimeout(ctx, c.commandTimeout)
	defer cancel()
	return c.runner(ctx, name, args...)
}

// Enclosure is one shelf. The identity fields are absent when sg_inq could
// not be read.
type Enclosure struct {
	Slot, Device                    string
	Vendor, Model, Revision, Serial Optional[string]
}

// Disk is one slot with a device in it.
type Disk struct {
	Enclosure string
	// Slot is the slot name up to the first comma, as printed.
	Slot string
	// SlotLabel is the enclosure's own name for the slot ("Slot 01,
	// front"), which is also how it appears under the sysfs root; SetLED
	// needs it to find the LED attributes.
	SlotLabel string
	Device    string
	// Map is the block device of this slot, when sg_map knows one.
	Map Optional[string]
	// The remaining fields are only filled in with DiskOptions.WithTelemetry
	// and stay absent when the device did not answer.
	Vendor, Model, Serial, Firmware Optional[string]
	Temperature                     Optional[int64]
}

// Fan is one cooling element with its last reported speed.
type Fan struct {
	Slot        string
	Serial      Optional[string]
	Description string
	Index       string
	Comment     Optional[string]
	Speed       int64
}

// DiskOptions selects how much work Disks does.
type DiskOptions struct {
	// WithTelemetry also reads vendor, model, serial, temperature and
	// firmware, at the cost of two external commands per disk.
	WithTelemetry bool
}

// LEDKind is the LED an enclosure exposes per slot. The values are the sysfs
// attribute names.
type LEDKind string

const (
	LEDLocate LEDKind = "locate"
	LEDFault  LEDKind = "fault"
)

func (k LEDKind) valid() bool { return k == LEDLocate || k == LEDFault }

// field returns the value of a "Key: value" line, if the output has one.
func field(s, key string) (string, bool) {
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
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
		result = append(result, Enclosure{Slot: slot, Device: device})
	}
	// One sg_inq per shelf, in parallel: they are independent devices.
	forEach(ctx, c.concurrency, len(result), func(i int) {
		details, err := c.exec(ctx, "sg_inq", result[i].Device)
		if err != nil {
			// The identity fields stay absent, which is survivable for the
			// exporter; the CLI would print a table of NONE.
			p.fail(collectorEnclosures, err)
			return
		}
		result[i].Vendor = From(field(details, "Vendor identification:"))
		result[i].Model = From(field(details, "Product identification:"))
		result[i].Revision = From(field(details, "Product revision level:"))
		result[i].Serial = From(field(details, "Unit serial number:"))
	})
	return result, nil
}

// readText reads a sysfs attribute holding a single line of text.
func readText(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(b), "�")), true
}

// serial decodes the serial number from VPD page 0x80, which is binary: a
// four-byte header with a big-endian length, then the number.
func serial(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if len(b) < 4 || b[1] != 0x80 {
		return "", false
	}
	n := int(binary.BigEndian.Uint16(b[2:4]))
	if n > len(b)-4 {
		return "", false
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(b[4:4+n]), "�")), true
}

var number = regexp.MustCompile(`-?\d+`)

// temperature extracts the current temperature in degrees Celsius from
// scsi_temperature output.
func temperature(out string) (int64, bool) {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(strings.ToLower(line), "current") && strings.Contains(strings.ToLower(line), "temperature") {
			_, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			n := number.FindString(value)
			if n == "" {
				continue
			}
			if v, err := strconv.ParseInt(n, 10, 64); err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

// Disks lists the disks of the given enclosures. Enclosures whose sysfs tree
// cannot be read are reported as an error; the disks that were found stay in
// the result.
func (c *Client) Disks(ctx context.Context, enclosures []Enclosure, opts DiskOptions) ([]Disk, error) {
	p := newProblems()
	ds := c.disks(ctx, enclosures, opts, p)
	if err := p.err(); err != nil {
		return ds, err
	}
	return ds, ctx.Err()
}

func (c *Client) disks(ctx context.Context, enclosures []Enclosure, opts DiskOptions, p *problems) []Disk {
	if len(enclosures) == 0 {
		return nil
	}
	mapping := map[string]string{}
	out, err := c.exec(ctx, "sg_map")
	if err != nil {
		// Without sg_map the block device of a slot is unknown, which only
		// costs the Map column; enumeration itself comes from sysfs.
		p.fail(collectorDisks, err)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 {
			mapping[f[0]] = f[1]
		}
	}
	var result []Disk
	for _, enc := range enclosures {
		base := filepath.Join(c.sysfs, enc.Slot)
		entries, err := os.ReadDir(base)
		if err != nil {
			// One unreadable shelf must not hide the other shelves (A6).
			p.fail(collectorDisks, fmt.Errorf("read enclosure sysfs: %w", err))
			continue
		}
		for _, entry := range entries {
			devPath := filepath.Join(base, entry.Name(), "device")
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
					Enclosure: enc.Slot,
					Slot:      strings.SplitN(entry.Name(), ",", 2)[0],
					SlotLabel: entry.Name(),
					Device:    "/dev/" + g.Name(),
				}
				if m, ok := mapping[d.Device]; ok && m != "" {
					d.Map = Some(m)
				}
				result = append(result, d)
			}
		}
	}
	if opts.WithTelemetry {
		// Two external commands per disk: strictly sequential this is what
		// makes a 60-slot shelf miss its scrape deadline (B1).
		forEach(ctx, c.concurrency, len(result), func(i int) {
			c.telemetry(ctx, &result[i], p)
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

// telemetry fills in the optional per-disk fields. Every failure leaves the
// field absent, so a disk with a dead sensor is still listed.
func (c *Client) telemetry(ctx context.Context, d *Disk, p *problems) {
	devPath := filepath.Join(c.sysfs, d.Enclosure, d.SlotLabel, "device")
	d.Vendor = From(readText(filepath.Join(devPath, "vendor")))
	d.Model = From(readText(filepath.Join(devPath, "model")))
	d.Serial = From(serial(filepath.Join(devPath, "vpd_pg80")))
	if out, err := c.exec(ctx, "scsi_temperature", d.Device); err != nil {
		p.note(collectorDisks, err)
	} else {
		d.Temperature = From(temperature(out))
	}
	if out, err := c.exec(ctx, "sginfo", d.Device); err != nil {
		p.note(collectorDisks, err)
	} else {
		d.Firmware = From(field(out, "Revision level:"))
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

// element is a cooling element found in the element listing, before its speed
// is known.
type element struct {
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
	lists := make([][]element, len(enclosures))
	forEach(ctx, c.concurrency, len(enclosures), func(i int) {
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
			lists[i] = append(lists[i], element{enc: enclosures[i], description: strings.TrimSpace(m[1]), index: m[2]})
		}
	})
	// Deduplicate in enclosure order so the result does not depend on which
	// goroutine finished first.
	var found []element
	seen := map[string]bool{}
	for _, list := range lists {
		for _, e := range list {
			// A shelf that does not report a serial number is identified by
			// its SCSI address instead.
			key := e.enc.Serial.Or(e.enc.Slot) + "\x00" + e.index
			if seen[key] {
				continue
			}
			seen[key] = true
			found = append(found, e)
		}
	}
	// Pass 2: one sg_ses per element, in parallel; results are written by
	// index so the order above is preserved.
	result := make([]Fan, len(found))
	ok := make([]bool, len(found))
	forEach(ctx, c.concurrency, len(found), func(i int) {
		e := found[i]
		out, err := c.exec(ctx, "sg_ses", "--index="+e.index, e.enc.Device)
		if err != nil {
			p.note(collectorFans, err)
			return
		}
		match := rpm.FindStringSubmatch(out)
		if match == nil {
			// A single sensor without an RPM line used to fail the scrape
			// and take the temperatures down with it (A6).
			p.note(collectorFans, fmt.Errorf("no fan RPM for %s index %s", e.enc.Device, e.index))
			return
		}
		speed, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			p.note(collectorFans, err)
			return
		}
		result[i] = Fan{
			Slot:        e.enc.Slot,
			Serial:      e.enc.Serial,
			Description: e.description,
			Index:       e.index,
			Comment:     comment(out),
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

// comment extracts the status word that sg_ses prints after the speed, as in
// "speed code: 2, Actual speed: 1200 rpm, low speed".
func comment(out string) Optional[string] {
	result := None[string]()
	for _, line := range strings.Split(out, "\n") {
		if !rpm.MatchString(line) {
			continue
		}
		if parts := strings.SplitN(line, ",", 3); len(parts) == 3 {
			result = Some(strings.TrimSpace(parts[2]))
		}
	}
	return result
}

// SetLED turns the locate or fault LED of one device on or off.
//
// The client resolves the sysfs attribute from its own root, so the paths
// stay out of the domain model (C3), and it only opens an attribute that
// already exists: it never creates a file.
func (c *Client) SetLED(ctx context.Context, device string, kind LEDKind, on bool) error {
	if !kind.valid() {
		return fmt.Errorf("unknown LED kind %q", string(kind))
	}
	if !strings.HasPrefix(device, "/dev/") {
		return fmt.Errorf("device %q must start with /dev/", device)
	}
	enclosures, err := c.Enclosures(ctx)
	if err != nil {
		return err
	}
	disks, err := c.Disks(ctx, enclosures, DiskOptions{})
	if err != nil {
		return err
	}
	for _, d := range disks {
		// device is a /dev/ path, so a plain comparison is enough: it can
		// never collide with a missing mapping.
		if d.Device != device && d.Map.Or("") != device {
			continue
		}
		path := filepath.Join(c.sysfs, d.Enclosure, d.SlotLabel, string(kind))
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s does not expose the %s LED", device, kind)
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
