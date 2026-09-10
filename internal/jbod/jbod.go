// SPDX-License-Identifier: BSD-2-Clause
// Copyright (c) 2021-2023, Gandi S.A.S.
// Go port of Gandi/jbod-rs.

// Package jbod reads storage enclosures on Linux: the slots and disks the
// enclosure driver exposes under sysfs, and the identity, temperature and fan
// speeds reported by the sg3-utils.
//
// The domain types and the collection live here, the untrusted output is
// parsed in parse.go, encoding to Prometheus text format is
// internal/metrics, and serving it over HTTP is internal/exporter.
package jbod

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	logger         *slog.Logger
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

// WithLogger sets where the client reports failed commands and finished
// collections. Without it the client stays silent, as a library should.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l
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
		logger:         slog.New(slog.DiscardHandler),
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
	// LEDLocate is the identify LED an operator uses to find a slot.
	LEDLocate LEDKind = "locate"
	// LEDFault is the fault LED.
	LEDFault LEDKind = "fault"
)

func (k LEDKind) valid() bool { return k == LEDLocate || k == LEDFault }

// Enclosures lists the enclosures. Failures on individual enclosures are
// reported as an error, but the enclosures that were read stay in the result.
func (c *Client) Enclosures(ctx context.Context) ([]Enclosure, error) {
	p := newProblems(c.logger)
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
	refs, errs := parseLsscsi(out)
	for _, err := range errs {
		p.fail(CollectorEnclosures, err)
	}
	result := make([]Enclosure, len(refs))
	for i, ref := range refs {
		result[i] = Enclosure{Slot: ref.Slot, Device: ref.Device}
	}
	// One sg_inq per shelf, in parallel: they are independent devices.
	forEach(ctx, c.concurrency, len(result), func(i int) {
		details, err := c.exec(ctx, "sg_inq", result[i].Device)
		if err != nil {
			// The identity fields stay absent, which is survivable for the
			// exporter; the CLI would print a table of NONE.
			p.fail(CollectorEnclosures, err)
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

// vpdSerial reads the serial number from a slot's VPD page 0x80 attribute.
func vpdSerial(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return parseVPD80(b)
}

// Disks lists the disks of the given enclosures. Enclosures whose sysfs tree
// cannot be read are reported as an error; the disks that were found stay in
// the result.
func (c *Client) Disks(ctx context.Context, enclosures []Enclosure, opts DiskOptions) ([]Disk, error) {
	p := newProblems(c.logger)
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
	out, err := c.exec(ctx, "sg_map")
	if err != nil {
		// Without sg_map the block device of a slot is unknown, which only
		// costs the Map column; enumeration itself comes from sysfs.
		p.fail(CollectorDisks, err)
	}
	mapping := parseSgMap(out)
	var result []Disk
	for _, enc := range enclosures {
		base := filepath.Join(c.sysfs, enc.Slot)
		entries, err := os.ReadDir(base)
		if err != nil {
			// One unreadable shelf must not hide the other shelves (A6).
			p.fail(CollectorDisks, fmt.Errorf("read enclosure sysfs: %w", err))
			continue
		}
		for _, entry := range entries {
			devPath := filepath.Join(base, entry.Name(), "device")
			generic, err := os.ReadDir(filepath.Join(devPath, "scsi_generic"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				p.fail(CollectorDisks, err)
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
	d.Serial = From(vpdSerial(filepath.Join(devPath, "vpd_pg80")))
	if out, err := c.exec(ctx, "scsi_temperature", d.Device); err != nil {
		p.note(CollectorDisks, err)
	} else {
		d.Temperature = From(parseTemperature(out))
	}
	if out, err := c.exec(ctx, "sginfo", d.Device); err != nil {
		p.note(CollectorDisks, err)
	} else {
		d.Firmware = From(field(out, "Revision level:"))
	}
}

// Fans lists the cooling elements of the given enclosures with their speed.
// A fan whose speed cannot be read is skipped rather than failing the whole
// listing; a shelf whose element listing fails is reported as an error.
func (c *Client) Fans(ctx context.Context, enclosures []Enclosure) ([]Fan, error) {
	p := newProblems(c.logger)
	fs := c.fans(ctx, enclosures, p)
	if err := p.err(); err != nil {
		return fs, err
	}
	return fs, ctx.Err()
}

// element is one cooling element together with the shelf it belongs to.
type element struct {
	enc Enclosure
	fan fanRef
}

func (c *Client) fans(ctx context.Context, enclosures []Enclosure, p *problems) []Fan {
	// Pass 1: element listing per shelf, in parallel.
	lists := make([][]fanRef, len(enclosures))
	forEach(ctx, c.concurrency, len(enclosures), func(i int) {
		out, err := c.exec(ctx, "sg_ses", "-j", "-ff", enclosures[i].Device)
		if err != nil {
			p.fail(CollectorFans, err)
			return
		}
		lists[i] = parseFanElements(out)
	})
	// Deduplicate in enclosure order so the result does not depend on which
	// goroutine finished first.
	var found []element
	seen := map[string]bool{}
	for i, list := range lists {
		for _, ref := range list {
			enc := enclosures[i]
			// A shelf that does not report a serial number is identified by
			// its SCSI address instead.
			key := enc.Serial.Or(enc.Slot) + "\x00" + ref.Index
			if seen[key] {
				continue
			}
			seen[key] = true
			found = append(found, element{enc: enc, fan: ref})
		}
	}
	// Pass 2: one sg_ses per element, in parallel; results are written by
	// index so the order above is preserved.
	result := make([]Fan, len(found))
	ok := make([]bool, len(found))
	forEach(ctx, c.concurrency, len(found), func(i int) {
		e := found[i]
		out, err := c.exec(ctx, "sg_ses", "--index="+e.fan.Index, e.enc.Device)
		if err != nil {
			p.note(CollectorFans, err)
			return
		}
		speed, condition, found := parseFanSpeed(out)
		if !found {
			// A single sensor without an RPM line used to fail the scrape
			// and take the temperatures down with it (A6).
			p.note(CollectorFans, fmt.Errorf("no fan RPM for %s index %s", e.enc.Device, e.fan.Index))
			return
		}
		result[i] = Fan{
			Slot:        e.enc.Slot,
			Serial:      e.enc.Serial,
			Description: e.fan.Description,
			Index:       e.fan.Index,
			Comment:     condition,
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
