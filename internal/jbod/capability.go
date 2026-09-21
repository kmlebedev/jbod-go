// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Capability discovery answers "what can this shelf actually do", separately
// for reading and for writing, and says what the answer is based on.
//
// Two rules shape everything here (ROADMAP 3):
//
//   - Discovery never changes the hardware. Nothing below writes, and
//     nothing opens an attribute for writing either; write support is judged
//     from the mode bits sysfs published, because an attribute the driver has
//     no store handler for is created read-only.
//
//   - Being able to read a page is not evidence that writing it works. The
//     enclosure may accept a control page and ignore it, and the kernel will
//     still return success, so write support is reported as unknown until a
//     real write has been confirmed by a readback. That is what the led
//     command does, and it is the only thing that can raise this to a fact.
//
// A transport or permission failure is kept apart from "unsupported": the
// first means we do not know, the second means the shelf said no.

// Support is what can be said about one direction of one capability.
type Support string

const (
	// SupportSupported means it was observed to work.
	SupportSupported Support = "supported"
	// SupportUnsupported means the interface is not there at all.
	SupportUnsupported Support = "unsupported"
	// SupportUnknown means it could not be determined without changing the
	// hardware, or the probe itself failed.
	SupportUnknown Support = "unknown"
)

// Capability is one thing the enclosure may let jbod-go do.
type Capability struct {
	// Name is the stable identifier, as "slot.power_status".
	Name string `json:"name"`
	// Summary says what the capability is, for the table.
	Summary string `json:"summary"`
	// Read and Write are the two directions, judged independently.
	Read  Support `json:"read"`
	Write Support `json:"write"`
	// Evidence names what the verdict rests on: a sysfs attribute, an SES
	// element listing, an installed tool.
	Evidence string `json:"evidence"`
	// Err is the probe failure, when there was one. It is reported apart
	// from the verdict so an EACCES is never read as "the shelf cannot do
	// this".
	Err Optional[string] `json:"error"`
}

// EnclosureCapabilities is the capability report for one shelf.
type EnclosureCapabilities struct {
	// Enclosure is the SCSI address, Address is what to pass to
	// --enclosure, and StableID says whether Address survives a reboot.
	Enclosure   string           `json:"enclosure"`
	EnclosureID Optional[string] `json:"enclosure_id"`
	Address     string           `json:"address"`
	StableID    bool             `json:"stable_id"`
	// Components is how many slots the walk found.
	Components int `json:"components"`
	// Shared lists the other sysfs enclosures that answer to the same
	// identifier, empty when this shelf is reached through one path only.
	// Two I/O modules of one chassis report the same logical identifier,
	// so a report that did not say this would read as two shelves.
	Shared       []string     `json:"shared,omitempty"`
	Capabilities []Capability `json:"capabilities"`
}

// writeNotProven is the evidence line for every writable attribute. It is
// the same sentence everywhere on purpose: the reason is always the same.
const writeNotProven = "the attribute is writable (%d/%d components), but a control page the enclosure ignores still returns success; only a readback after a real write confirms it"

// Capabilities reports what each of the given enclosures can do.
//
// It runs one element listing per shelf and reads sysfs attributes; it
// performs no writes and starts no self-tests.
func (c *Client) Capabilities(ctx context.Context, enclosures []Enclosure) ([]EnclosureCapabilities, error) {
	p := newProblems(c.logger)
	result := make([]EnclosureCapabilities, len(enclosures))
	// One element listing per shelf, in parallel: the shelves are
	// independent devices, and a rack of them should not take as long as
	// the sum of their timeouts.
	forEach(ctx, c.concurrency, len(enclosures), func(i int) {
		enc := enclosures[i]
		address, stable := enc.Ref()
		report := EnclosureCapabilities{
			Enclosure:   enc.Slot,
			EnclosureID: enc.ID,
			Address:     address,
			StableID:    stable,
		}
		dirs, err := c.componentDirs(enc.Slot)
		if err != nil {
			p.fail(CollectorSlots, fmt.Errorf("read enclosure sysfs: %w", err))
		}
		report.Components = len(dirs)
		for _, sibling := range SiblingsOf(enclosures, i) {
			report.Shared = append(report.Shared, sibling.Slot)
		}
		report.Capabilities = c.probe(ctx, enc, dirs)
		result[i] = report
	})
	return result, p.err()
}

// probe assembles the capability list for one shelf.
func (c *Client) probe(ctx context.Context, enc Enclosure, dirs []string) []Capability {
	caps := []Capability{
		fileCapability("enclosure.id", "stable enclosure identifier", filepath.Join(c.sysfs, enc.Slot, "id")),
		fileCapability("enclosure.components", "declared component count", filepath.Join(c.sysfs, enc.Slot, "components")),
		c.identityCapability(enc),
		slotEnumeration(dirs),
		attributeCapability("slot.number", "slot number reported by the enclosure", dirs, "slot"),
		attributeCapability("slot.type", "component type", dirs, "type"),
		attributeCapability("slot.status", "component status", dirs, "status"),
		attributeCapability("slot.power_status", "slot power state", dirs, "power_status"),
		attributeCapability("led.locate", "identify indicator", dirs, "locate"),
		attributeCapability("led.fault", "fault indicator", dirs, "fault"),
		c.fanCapability(ctx, enc),
		c.toolCapability("disk.temperature", "per-disk temperature", "scsi_temperature", false),
		c.toolCapability("disk.firmware", "per-disk firmware revision", "sginfo", false),
	}
	return caps
}

// slotEnumeration reports whether the shelf exposes slots at all. It is the
// capability everything else in the inventory depends on.
func slotEnumeration(dirs []string) Capability {
	entry := Capability{
		Name:    "slot.enumeration",
		Summary: "every slot, empty ones included",
		// Enumeration is a directory walk; there is nothing to write.
		Write: SupportUnsupported,
	}
	if len(dirs) == 0 {
		entry.Read = SupportUnsupported
		entry.Evidence = "the enclosure directory exposes no components"
		return entry
	}
	entry.Read = SupportSupported
	entry.Evidence = fmt.Sprintf("sysfs: %d component directories", len(dirs))
	return entry
}

// fileCapability classifies a single sysfs file of the enclosure itself.
func fileCapability(name, summary, path string) Capability {
	entry := Capability{Name: name, Summary: summary, Write: SupportUnsupported}
	info, err := os.Stat(path)
	switch {
	case err != nil && os.IsNotExist(err):
		entry.Read = SupportUnsupported
		entry.Evidence = "no " + filepath.Base(path) + " attribute"
		return entry
	case err != nil:
		entry.Read = SupportUnknown
		entry.Evidence = "sysfs: " + filepath.Base(path)
		entry.Err = Some(err.Error())
		return entry
	}
	if _, ok := readText(path); !ok {
		entry.Read = SupportUnknown
		entry.Evidence = "sysfs: " + filepath.Base(path) + " exists but could not be read"
		return entry
	}
	entry.Read = SupportSupported
	entry.Evidence = "sysfs: " + filepath.Base(path)
	if info.Mode()&0o222 != 0 {
		entry.Write = SupportUnknown
		entry.Evidence += "; " + fmt.Sprintf(writeNotProven, 1, 1)
	}
	return entry
}

// attributeCapability classifies one per-component attribute across every
// component of a shelf.
//
// Read is supported when every component that has the attribute could be
// read, and unknown when some could not: a slot we may not read is not a
// slot without the feature. Write follows the mode bits, and stops at
// unknown by design.
func attributeCapability(name, summary string, dirs []string, attribute string) Capability {
	entry := Capability{Name: name, Summary: summary}
	var present, readable, writable int
	var failure Optional[string]
	for _, dir := range dirs {
		path := filepath.Join(dir, attribute)
		info, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) && !failure.Present() {
				failure = Some(err.Error())
			}
			continue
		}
		present++
		if _, ok := readText(path); ok {
			readable++
		} else if !failure.Present() {
			failure = Some(path + ": present but unreadable")
		}
		if info.Mode()&0o222 != 0 {
			writable++
		}
	}
	entry.Err = failure
	switch {
	case len(dirs) == 0:
		entry.Read, entry.Write = SupportUnknown, SupportUnknown
		entry.Evidence = "no components to inspect"
		return entry
	case present == 0:
		entry.Read, entry.Write = SupportUnsupported, SupportUnsupported
		entry.Evidence = fmt.Sprintf("no component exposes %s", attribute)
		return entry
	case readable == present:
		entry.Read = SupportSupported
	default:
		entry.Read = SupportUnknown
	}
	entry.Evidence = fmt.Sprintf("sysfs: %d/%d components expose %s, %d readable", present, len(dirs), attribute, readable)
	if writable == 0 {
		entry.Write = SupportUnsupported
		entry.Evidence += "; read-only, the driver declares no store handler"
		return entry
	}
	entry.Write = SupportUnknown
	entry.Evidence += "; " + fmt.Sprintf(writeNotProven, writable, present)
	return entry
}

// fanCapability asks the shelf for its cooling elements. Listing elements is
// a read of the SES status pages and changes nothing.
func (c *Client) fanCapability(ctx context.Context, enc Enclosure) Capability {
	entry := Capability{Name: "fan.rpm", Summary: "cooling element speeds", Write: SupportUnsupported}
	out, err := c.exec(ctx, "sg_ses", "-j", "-ff", enc.Device)
	if err != nil {
		// A tool that is missing, a device that is busy and a shelf that
		// rejects the page all land here, and none of them mean the shelf
		// has no fans.
		entry.Read = SupportUnknown
		entry.Evidence = "sg_ses -j -ff " + enc.Device
		entry.Err = Some(err.Error())
		return entry
	}
	elements := parseFanElements(out)
	if len(elements) == 0 {
		entry.Read = SupportUnsupported
		entry.Evidence = "sg_ses reports no cooling elements"
		return entry
	}
	entry.Read = SupportSupported
	entry.Evidence = fmt.Sprintf("sg_ses: %d cooling elements", len(elements))
	return entry
}

// identityCapability reports whether sg_inq answered for this shelf. The
// answer is already in hand from discovery, so nothing is run again; when
// it failed, the reason travels with the shelf and is reported apart from
// the verdict.
func (c *Client) identityCapability(enc Enclosure) Capability {
	entry := Capability{
		Name:    "enclosure.identity",
		Summary: "vendor, model, revision and serial",
		Write:   SupportUnsupported,
	}
	if enc.Vendor.Present() || enc.Serial.Present() {
		entry.Read = SupportSupported
		entry.Evidence = "sg_inq: answered during discovery"
		return entry
	}
	if err, ok := enc.Err.Get(); ok {
		entry.Read = SupportUnknown
		entry.Evidence = "sg_inq " + enc.Device
		entry.Err = Some(err)
		return entry
	}
	entry.Read = SupportUnsupported
	entry.Evidence = "sg_inq answered without an identity"
	return entry
}

// toolCapability reports a capability that depends on an external tool.
//
// observed lets the caller raise the verdict when the value is already in
// hand, as it is for sg_inq after enclosure discovery. Otherwise an
// installed tool only makes the capability unknown: whether a particular
// drive answers is a per-device fact, and finding out for every drive is
// what the listing commands are for.
func (c *Client) toolCapability(name, summary, tool string, observed bool) Capability {
	entry := Capability{Name: name, Summary: summary, Write: SupportUnsupported}
	if observed {
		entry.Read = SupportSupported
		entry.Evidence = tool + ": answered during discovery"
		return entry
	}
	if c.tools == nil {
		entry.Read = SupportUnknown
		entry.Evidence = tool + ": command execution is injected, tool presence unknown"
		return entry
	}
	if _, err := c.tools.path(tool); err != nil {
		entry.Read = SupportUnsupported
		entry.Evidence = tool + ": not installed"
		entry.Err = Some(err.Error())
		return entry
	}
	entry.Read = SupportUnknown
	entry.Evidence = tool + ": installed; per-device support is reported by the listing commands"
	return entry
}

// Supported reports whether every named capability is readable. It is what a
// command uses before promising output it cannot produce.
func (r EnclosureCapabilities) Supported(names ...string) bool {
	for _, name := range names {
		found := false
		for _, entry := range r.Capabilities {
			if entry.Name == name {
				found = entry.Read == SupportSupported
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// String renders a capability the way the table does, for logs and test
// failures.
func (x Capability) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s read=%s write=%s (%s)", x.Name, x.Read, x.Write, x.Evidence)
	if err, ok := x.Err.Get(); ok {
		fmt.Fprintf(&b, " err=%s", err)
	}
	return b.String()
}
