// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// This file models the enclosure the way the hardware does: a shelf has
// slots, and a slot may or may not have a disk in it. Before v1.1 only
// occupied slots existed, because enumeration started from
// device/scsi_generic, so an empty bay was indistinguishable from one whose
// drive had failed out of the topology (ROADMAP 4).
//
// Everything here comes from the Linux enclosure class
// (drivers/misc/enclosure.c) and, for shelves behind it, the SES backend
// (drivers/scsi/ses.c). The attribute names and value spellings below are
// theirs.

// Occupancy says whether a slot has a device in it.
//
// The three states are deliberately distinct: an empty bay, a bay with a
// device, and a bay we could not read are three different operational
// situations, and collapsing the third into either of the others is how a
// missing drive becomes invisible.
type Occupancy string

const (
	// OccupancyOccupied means a device is attached to the slot.
	OccupancyOccupied Occupancy = "occupied"
	// OccupancyEmpty means nothing is attached and the enclosure answered:
	// it either reports "not installed" or reports some other status.
	OccupancyEmpty Occupancy = "empty"
	// OccupancyUnavailable means the slot could not be read, or the
	// enclosure reports the component as unavailable or unsupported. It is
	// not the same as empty and must never be rendered as one.
	OccupancyUnavailable Occupancy = "unavailable"
)

// Status spellings of the Linux enclosure class, from the enclosure_status
// table in drivers/misc/enclosure.c.
const (
	statusNotInstalled = "not installed"
	statusUnavailable  = "unavailable"
	statusUnsupported  = "unsupported"
)

// FaultState is the fault indication of a slot, split into what the
// enclosure detected and what somebody asked for.
//
// The SES backend packs both into one sysfs value: ses.c stores
// (status[3] & 0x60) >> 5 into the fault attribute, and in the SES-3 device
// slot status element bit 6 is FAULT SENSED while bit 5 is RQST FAULT. So 2
// is a fault the shelf detected, 1 is a fault LED somebody turned on, and 3
// is both. A backend other than SES reports a plain 0 or 1, which can only
// be read as requested.
//
// The distinction matters for the LED commands: a write sets the requested
// bit only (ses_set_fault ORs in 0x20), so a readback has to compare that
// bit and not the whole value.
type FaultState struct {
	// Value is the raw attribute, absent when there is no fault attribute.
	Value Optional[int64] `json:"value"`
	// Sensed is the fault the enclosure itself detected.
	Sensed Optional[bool] `json:"sensed"`
	// Requested is the fault indication somebody asked for.
	Requested Optional[bool] `json:"requested"`
}

// parseFault decodes the fault attribute. ok is false when the attribute is
// missing or unreadable, which leaves every field absent.
func parseFault(raw string, ok bool) FaultState {
	if !ok {
		return FaultState{}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return FaultState{}
	}
	return FaultState{
		Value:     Some(n),
		Sensed:    Some(n&0x2 != 0),
		Requested: Some(n&0x1 != 0),
	}
}

// Any reports whether either bit is set. A slot with no fault attribute
// reports false, which callers must read together with Value.Present.
func (f FaultState) Any() bool {
	return f.Sensed.Or(false) || f.Requested.Or(false)
}

// Slot is one component of an enclosure: a physical bay, with or without a
// device in it.
//
// The identity fields come straight from sysfs and are absent when the
// enclosure does not expose the attribute, which is common: a shelf behind a
// plain enclosure driver has neither slot numbers nor power status.
type Slot struct {
	// Enclosure is the SCSI address of the shelf, which is also the sysfs
	// directory name. It changes across reboots; see EnclosureID.
	Enclosure string `json:"enclosure"`
	// EnclosureID is the enclosure logical identifier, which does not.
	EnclosureID Optional[string] `json:"enclosure_id"`
	// Name is the enclosure's own name for the component ("Slot 01,
	// front") and the sysfs directory under the shelf.
	Name string `json:"name"`
	// Label is Name up to the first comma, which is what the tables print.
	Label string `json:"label"`
	// Number is the slot number the enclosure reports, from the "slot"
	// attribute. It is the stable half of an address; Name is cosmetic.
	Number Optional[int64] `json:"number"`
	// Type is what the enclosure calls the component ("array device").
	Type Optional[string] `json:"type"`
	// Status is the component status, spelled as the enclosure class does.
	Status Optional[string] `json:"status"`
	// Occupancy classifies the slot; see the constants.
	Occupancy Occupancy `json:"occupancy"`
	// Locate is the identify LED, absent when there is no such attribute.
	Locate Optional[bool] `json:"locate"`
	// Fault is the fault indication, split into sensed and requested.
	Fault FaultState `json:"fault"`
	// Power is the slot power status ("on", "off", "unknown").
	Power Optional[string] `json:"power_status"`
	// Device is the generic device of the disk in this slot, when there is
	// one. A slot with a device but no scsi_generic node stays occupied
	// with Device absent.
	Device Optional[string] `json:"device"`
	// Map is the block device of the disk in this slot, from sg_map.
	Map Optional[string] `json:"map"`
	// Err explains an unavailable slot.
	Err Optional[string] `json:"error"`
}

// Address returns how to address this slot on the command line: the stable
// enclosure identifier when the shelf has one, and the slot number when the
// enclosure reports one.
func (s Slot) Address() string {
	slot := s.Label
	if n, ok := s.Number.Get(); ok {
		slot = strconv.FormatInt(n, 10)
	}
	return s.EnclosureID.Or(s.Enclosure) + "/" + slot
}

// componentSkip are the entries of an enclosure directory that are never
// components: the back-pointer to the SCSI device and the two directories
// the driver core adds. The SCSI device also has a "type" attribute, so it
// has to be excluded by name rather than by what it contains.
var componentSkip = map[string]bool{"device": true, "power": true, "subsystem": true}

// componentAttrs are the entries that mark a directory as an enclosure
// component: the per-component attributes of the enclosure class, plus the
// link to the device in the slot.
//
// The kernel always creates "type", but a driver that exposes fewer
// attributes, or a slot that only carries a device link, still has to be
// enumerated: a bay this walk does not see is a bay the operator cannot
// address. The enclosure's own "device" link is excluded by name in
// componentSkip, so it cannot match here.
var componentAttrs = []string{"type", "slot", "status", "locate", "fault", "active", "power_status", "device"}

// isComponent reports whether dir looks like an enclosure component.
func isComponent(dir string) bool {
	for _, name := range componentAttrs {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// componentNames picks the component directories out of an enclosure
// directory listing, in the order sysfs returned them.
func componentNames(base string, entries []os.DirEntry) []string {
	var names []string
	for _, entry := range entries {
		if componentSkip[entry.Name()] {
			continue
		}
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		if !isComponent(filepath.Join(base, entry.Name())) {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}

// componentDirs lists the component directories of one shelf.
func (c *Client) componentDirs(enc string) ([]string, error) {
	base := filepath.Join(c.sysfs, enc)
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}
	names := componentNames(base, entries)
	dirs := make([]string, len(names))
	for i, name := range names {
		dirs[i] = filepath.Join(base, name)
	}
	return dirs, nil
}

// enclosureID reads the logical identifier of a shelf, which the enclosure
// class exposes as "id" and the SES backend fills from the enclosure
// logical identifier. It is the only identifier here that survives a reboot
// or a recabling; the SCSI address does not.
func (c *Client) enclosureID(enc string) Optional[string] {
	id, ok := readText(filepath.Join(c.sysfs, enc, "id"))
	if !ok || id == "" {
		return None[string]()
	}
	return Some(id)
}

// readBool reads a sysfs attribute holding 0 or 1.
func readBool(path string) (bool, bool) {
	raw, ok := readText(path)
	if !ok {
		return false, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return false, false
	}
	return n != 0, true
}

// readInt reads a sysfs attribute holding a decimal number.
func readInt(path string) (int64, bool) {
	raw, ok := readText(path)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Slots lists every slot of the given enclosures, empty ones included.
//
// A shelf whose sysfs tree cannot be read is reported as an error and the
// other shelves are still returned; a single component that cannot be read
// becomes an unavailable slot rather than a failure, because a drive pulled
// during the walk must not take the listing down with it.
func (c *Client) Slots(ctx context.Context, enclosures []Enclosure) ([]Slot, error) {
	p := newProblems(c.logger)
	s := c.slots(ctx, enclosures, p)
	if err := p.err(); err != nil {
		return s, err
	}
	return s, ctx.Err()
}

func (c *Client) slots(ctx context.Context, enclosures []Enclosure, p *problems) []Slot {
	if len(enclosures) == 0 {
		return nil
	}
	// One sg_map for the whole pass: it maps generic devices to block
	// devices and knows nothing about slots.
	out, err := c.exec(ctx, "sg_map")
	if err != nil {
		p.fail(CollectorSlots, err)
	}
	mapping := parseSgMap(out)
	var result []Slot
	for _, enc := range enclosures {
		base := filepath.Join(c.sysfs, enc.Slot)
		entries, err := os.ReadDir(base)
		if err != nil {
			p.fail(CollectorSlots, fmt.Errorf("read enclosure sysfs: %w", err))
			continue
		}
		id := c.enclosureID(enc.Slot)
		for _, name := range componentNames(base, entries) {
			result = append(result, c.readSlot(enc, id, name, filepath.Join(base, name), mapping, p))
		}
	}
	slices.SortStableFunc(result, compareSlots)
	return result
}

// compareSlots orders slots by shelf and then by the number the enclosure
// reports, falling back to the component name for shelves that report none.
func compareSlots(a, b Slot) int {
	if n := natCompare(a.Enclosure, b.Enclosure); n != 0 {
		return n
	}
	an, aok := a.Number.Get()
	bn, bok := b.Number.Get()
	if aok && bok && an != bn {
		return cmp.Compare(an, bn)
	}
	return natCompare(a.Name, b.Name)
}

// readSlot fills in one component from its sysfs directory.
func (c *Client) readSlot(enc Enclosure, id Optional[string], name, dir string, mapping map[string]string, p *problems) Slot {
	s := Slot{
		Enclosure:   enc.Slot,
		EnclosureID: id,
		Name:        name,
		Label:       strings.SplitN(name, ",", 2)[0],
		Number:      From(readInt(filepath.Join(dir, "slot"))),
		Type:        From(readText(filepath.Join(dir, "type"))),
		Status:      From(readText(filepath.Join(dir, "status"))),
		Locate:      From(readBool(filepath.Join(dir, "locate"))),
		Fault:       parseFault(readText(filepath.Join(dir, "fault"))),
		Power:       From(readText(filepath.Join(dir, "power_status"))),
	}
	s.Occupancy, s.Device, s.Err = c.occupancy(dir, s.Status, p)
	if device, ok := s.Device.Get(); ok {
		if m, ok := mapping[device]; ok && m != "" {
			s.Map = Some(m)
		}
	}
	return s
}

// occupancy decides what is in the slot and returns the generic device when
// there is one.
//
// Reading the directory is the authority on whether something is attached;
// the status attribute only resolves what "nothing attached" means. An error
// other than "does not exist" is never flattened into empty: a slot we could
// not read is unavailable and says why.
func (c *Client) occupancy(dir string, status Optional[string], p *problems) (Occupancy, Optional[string], Optional[string]) {
	devPath := filepath.Join(dir, "device")
	generic, err := os.ReadDir(filepath.Join(devPath, "scsi_generic"))
	switch {
	case err == nil && len(generic) > 0:
		return OccupancyOccupied, Some("/dev/" + generic[0].Name()), None[string]()
	case err == nil:
		// A device is attached but exposes no generic node.
		return OccupancyOccupied, None[string](), None[string]()
	case !os.IsNotExist(err):
		// A drive pulled during the walk, or an attribute we may not read.
		// Either way this is not an empty bay (ROADMAP 4).
		p.note(CollectorSlots, err)
		return OccupancyUnavailable, None[string](), Some(err.Error())
	}
	// scsi_generic is gone; the device link itself decides whether anything
	// is attached at all.
	if _, err := os.Stat(devPath); err == nil {
		return OccupancyOccupied, None[string](), None[string]()
	}
	switch strings.ToLower(status.Or("")) {
	case statusUnavailable, statusUnsupported:
		return OccupancyUnavailable, None[string](), Some("enclosure reports the component as " + status.Or(""))
	}
	return OccupancyEmpty, None[string](), None[string]()
}

// genericDevices lists every generic device of one slot directory. A slot
// with two of them is unusual but the enclosure driver allows it, and Disks
// has always reported one disk per node.
func genericDevices(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, "device", "scsi_generic"))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, "/dev/"+entry.Name())
	}
	return names
}
