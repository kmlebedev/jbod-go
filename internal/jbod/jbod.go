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
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
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
	runner Runner
	// tools resolves and runs the external commands. It is nil when the
	// runner was injected, which is what tells Preflight that there are no
	// binaries to look for.
	tools              *tools
	sysfs              string
	commandTimeout     time.Duration
	concurrency        int
	ledReadbackTimeout time.Duration
	ledWriter          LEDWriter
	logger             *slog.Logger
}

// Option configures a Client. Values that make no sense (an empty sysfs
// path, a non-positive timeout) are ignored, so New always returns a usable
// client and an empty Sysfs cannot be a working state (C7).
type Option func(*Client)

// WithRunner replaces the external command execution; for tests. A client
// with an injected runner runs no binaries, so Preflight stops looking for
// them.
func WithRunner(r Runner) Option {
	return func(c *Client) {
		if r != nil {
			c.runner = r
			c.tools = nil
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

// WithLEDReadbackTimeout bounds how long a LED write waits for the
// enclosure to report the state it was asked for.
func WithLEDReadbackTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d >= 0 {
			c.ledReadbackTimeout = d
		}
	}
}

// WithLEDWriter replaces how a LED attribute is written; for tests. The
// readback still reads the real attribute, which is what lets a test model
// a shelf that accepts a write and does nothing.
func WithLEDWriter(w LEDWriter) Option {
	return func(c *Client) {
		if w != nil {
			c.ledWriter = w
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
	resolved := newTools()
	c := &Client{
		runner:             resolved.run,
		tools:              resolved,
		sysfs:              DefaultSysfs,
		commandTimeout:     DefaultCommandTimeout,
		concurrency:        DefaultConcurrency,
		ledReadbackTimeout: DefaultLEDReadbackTimeout,
		ledWriter:          writeAttribute,
		logger:             slog.New(slog.DiscardHandler),
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
	// Slot is the SCSI address, which is also the sysfs directory name. It
	// is assigned at scan time and changes across reboots and recabling,
	// so it is a location and not an identity.
	Slot   string `json:"enclosure"`
	Device string `json:"device"`
	// ID is the enclosure logical identifier from the sysfs "id" attribute,
	// which the SES backend fills from the enclosure descriptor. This is
	// the identifier that survives a reboot (ROADMAP 3).
	ID       Optional[string] `json:"id"`
	Vendor   Optional[string] `json:"vendor"`
	Model    Optional[string] `json:"model"`
	Revision Optional[string] `json:"revision"`
	Serial   Optional[string] `json:"serial"`
	// Err is why the identity is absent, when sg_inq failed. A shelf whose
	// vendor and model read as unknown should be able to say whether the
	// tool is missing, the device is busy or the shelf simply answered
	// nothing (ROADMAP 3).
	Err Optional[string] `json:"error"`
}

// Ref returns the identifier to address this shelf by, and whether it is
// stable. The logical identifier is preferred, the unit serial number is
// the fallback, and the SCSI address is a last resort that callers must
// mark as temporary rather than print as an identity (ROADMAP 3).
func (e Enclosure) Ref() (string, bool) {
	if id, ok := e.ID.Get(); ok {
		return id, true
	}
	if serial, ok := e.Serial.Get(); ok {
		return serial, true
	}
	return e.Slot, false
}

// Identifier sources, as reported by IDSource and by the id_source label of
// jbod_enclosure_info.
const (
	// IDSourceLogical is the enclosure logical identifier: stable.
	IDSourceLogical = "logical"
	// IDSourceSerial is the unit serial number: stable.
	IDSourceSerial = "serial"
	// IDSourceAddress is the SCSI address: a location, reassigned on every
	// scan, and only used when the shelf offers nothing better.
	IDSourceAddress = "address"
)

// IDSource names where Ref took the identifier from, so a consumer can see
// that an identity is really a temporary address (ROADMAP 3).
func (e Enclosure) IDSource() string {
	switch {
	case e.ID.Present():
		return IDSourceLogical
	case e.Serial.Present():
		return IDSourceSerial
	default:
		return IDSourceAddress
	}
}

// Matches reports whether ref addresses this shelf. Any of the three
// spellings is accepted so an operator can paste whichever one they have in
// front of them; the identifier comparison is case-insensitive because NAA
// identifiers are hex.
func (e Enclosure) Matches(ref string) bool {
	if ref == "" {
		return false
	}
	if strings.EqualFold(ref, e.Slot) {
		return true
	}
	for _, candidate := range []Optional[string]{e.ID, e.Serial} {
		if v, ok := candidate.Get(); ok && strings.EqualFold(ref, v) {
			return true
		}
	}
	return false
}

// SiblingsOf returns the other enclosures that answer to the same
// identifier as the one at index i.
//
// A chassis with two I/O modules registers one sysfs enclosure per module,
// and both report the same enclosure logical identifier, because it
// identifies the chassis and not the module. So an identifier does not
// address a single sysfs enclosure, and anything that resolves one has to
// say which path it picked.
func SiblingsOf(enclosures []Enclosure, i int) []Enclosure {
	if i < 0 || i >= len(enclosures) {
		return nil
	}
	ref, _ := enclosures[i].Ref()
	var siblings []Enclosure
	for j, other := range enclosures {
		if j == i {
			continue
		}
		if otherRef, _ := other.Ref(); strings.EqualFold(ref, otherRef) {
			siblings = append(siblings, other)
		}
	}
	return siblings
}

// SelectEnclosures keeps the shelves matching ref, or all of them when ref
// is empty. An unknown reference is an error rather than an empty listing,
// which would read as "this shelf has nothing in it".
func SelectEnclosures(enclosures []Enclosure, ref string) ([]Enclosure, error) {
	if ref == "" {
		return enclosures, nil
	}
	var kept []Enclosure
	for _, e := range enclosures {
		if e.Matches(ref) {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("no enclosure matches %q; jbod list --enclosure lists the known identifiers", ref)
	}
	return kept, nil
}

// Disk is one slot with a device in it.
type Disk struct {
	Enclosure string `json:"enclosure"`
	// EnclosureID is the stable identifier of the shelf, when it has one.
	EnclosureID Optional[string] `json:"enclosure_id"`
	// Slot is the slot name up to the first comma, as printed.
	Slot string `json:"slot"`
	// SlotNumber is the number the enclosure reports for this slot, which
	// is how the slot is addressed; Slot is the cosmetic name.
	SlotNumber Optional[int64] `json:"slot_number"`
	// SlotLabel is the enclosure's own name for the slot ("Slot 01,
	// front"), which is also how it appears under the sysfs root; the LED
	// commands need it to find the attributes.
	SlotLabel string `json:"slot_label"`
	Device    string `json:"device"`
	// Map is the block device of this slot, when sg_map knows one.
	Map Optional[string] `json:"map"`
	// The remaining fields are only filled in with DiskOptions.WithTelemetry
	// and stay absent when the device did not answer.
	Vendor      Optional[string] `json:"vendor"`
	Model       Optional[string] `json:"model"`
	Serial      Optional[string] `json:"serial"`
	Firmware    Optional[string] `json:"firmware"`
	Temperature Optional[int64]  `json:"temperature_celsius"`
}

// Fan is one cooling element with its last reported speed.
type Fan struct {
	Slot        string           `json:"enclosure"`
	Serial      Optional[string] `json:"enclosure_serial"`
	Description string           `json:"component"`
	Index       string           `json:"component_id"`
	Comment     Optional[string] `json:"condition"`
	// Speed is absent when the element answered without an RPM reading.
	// It used to be dropped from the listing entirely, so a cooling
	// element that stopped reporting simply vanished from a table of
	// eight fans and nobody could tell it had ever been there.
	Speed Optional[int64] `json:"speed_rpm"`
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
		// The logical identifier is a sysfs read, so it is available even
		// when sg_inq below fails or the tool is missing.
		result[i] = Enclosure{Slot: ref.Slot, Device: ref.Device, ID: c.enclosureID(ref.Slot)}
	}
	// One sg_inq per shelf, in parallel: they are independent devices.
	forEach(ctx, c.concurrency, len(result), func(i int) {
		details, err := c.exec(ctx, "sg_inq", result[i].Device)
		if err != nil {
			// The identity fields stay absent, which is survivable for the
			// exporter; the CLI would print a table of NONE. The reason is
			// kept on the shelf so the capability report can tell a failed
			// probe from an unsupported one.
			result[i].Err = Some(err.Error())
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
	return c.disksFromSlots(ctx, c.slots(ctx, enclosures, p), opts, p)
}

// disksFromSlots projects the occupied slots onto the disk view.
//
// Enumeration used to start from device/scsi_generic, which is why an empty
// bay did not exist; it starts from the slot walk now, and the disk list is
// a view of it so the two can never disagree (ROADMAP 4).
func (c *Client) disksFromSlots(ctx context.Context, slots []Slot, opts DiskOptions, p *problems) []Disk {
	var result []Disk
	for _, s := range slots {
		if s.Occupancy != OccupancyOccupied {
			continue
		}
		// One disk per generic node, as before: a slot may expose more than
		// one, and Slot.Device only names the first.
		devices := genericDevices(filepath.Join(c.sysfs, s.Enclosure, s.Name))
		if len(devices) == 0 {
			// A device is attached but has no generic node, so there is
			// nothing to address it by; it stays a slot and not a disk.
			continue
		}
		for _, device := range devices {
			d := Disk{
				Enclosure:   s.Enclosure,
				EnclosureID: s.EnclosureID,
				Slot:        s.Label,
				SlotNumber:  s.Number,
				SlotLabel:   s.Name,
				Device:      device,
			}
			if device == s.Device.Or("") {
				d.Map = s.Map
			}
			result = append(result, d)
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
		an, aok := a.SlotNumber.Get()
		bn, bok := b.SlotNumber.Get()
		if aok && bok && an != bn {
			return cmp.Compare(an, bn)
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
		lists[i] = individual(parseFanElements(out))
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
	forEach(ctx, c.concurrency, len(found), func(i int) {
		e := found[i]
		result[i] = Fan{
			Slot:        e.enc.Slot,
			Serial:      e.enc.Serial,
			Description: e.fan.Description,
			Index:       e.fan.Index,
		}
		out, err := c.exec(ctx, "sg_ses", "--index="+e.fan.Index, e.enc.Device)
		if err != nil {
			p.note(CollectorFans, err)
			return
		}
		speed, condition, ok := parseFanSpeed(out)
		result[i].Comment = condition
		if !ok {
			// A single sensor without an RPM line used to fail the scrape
			// and take the temperatures down with it (A6). The element
			// stays in the listing with its speed absent, so a fan that
			// stopped answering is visible rather than missing.
			p.note(CollectorFans, fmt.Errorf("no fan RPM for %s index %s", e.enc.Device, e.fan.Index))
			return
		}
		result[i].Speed = Some(speed)
	})
	if len(result) == 0 {
		return nil
	}
	return result
}
