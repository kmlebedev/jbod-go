// SPDX-License-Identifier: BSD-2-Clause

package jbod

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultLEDReadbackTimeout is how long a write waits for the enclosure to
// report the state it was asked for. A SES control page goes out over the
// expander and the next status read has to come back, so the answer is not
// instant; a second is generous for that round trip and short enough that a
// shelf which simply ignores the page does not hang the command.
const DefaultLEDReadbackTimeout = time.Second

// ledReadbackInterval is how often the attribute is re-read while waiting.
const ledReadbackInterval = 20 * time.Millisecond

// Errors the LED operations return, so callers can tell a shelf that
// refused from hardware that went away mid-operation.
var (
	// ErrSlotGone reports that the slot disappeared while it was being
	// written: a drive pulled between the listing and the write, or a
	// shelf that dropped off the bus (ROADMAP 4).
	ErrSlotGone = errors.New("the slot disappeared during the operation")
	// ErrLEDNotApplied reports that the write was accepted by the kernel
	// but the enclosure still reports the old state. A system call that
	// returns success is not evidence that an indicator changed, and this
	// is what keeps the two apart (ROADMAP 4).
	ErrLEDNotApplied = errors.New("the enclosure did not apply the requested LED state")
	// ErrNoSuchTarget reports that nothing in the inventory matches.
	ErrNoSuchTarget = errors.New("no slot matches the target")
)

// LEDWriter writes one value into one sysfs attribute.
//
// It is indirected for the same reason Runner is: the failure this readback
// exists for — an enclosure that accepts a control page and then ignores it
// — cannot be reproduced with a temporary file, because writing to a file
// always changes what reading it returns. A test supplies a writer that
// drops the value the way such a shelf does.
type LEDWriter func(path, value string) error

// LEDTarget addresses one indicator: either a device path, as the original
// CLI has always accepted, or a slot of an enclosure, which is the only way
// to reach a bay with no disk in it.
type LEDTarget struct {
	// Raw is the target as it was written, for messages.
	Raw string
	// Device is a /dev/ path, when the target names one.
	Device string
	// Enclosure is the shelf reference: a logical identifier, a unit serial
	// number or a SCSI address. Empty means "any shelf".
	Enclosure string
	// Slot is the slot reference: the number the enclosure reports, or the
	// component name.
	Slot string
}

// IsDevice reports whether the target names a device path.
func (t LEDTarget) IsDevice() bool { return t.Device != "" }

// String renders the target the way it was written.
func (t LEDTarget) String() string { return t.Raw }

// ParseLEDTarget interprets one --locate/--fault value.
//
// Three spellings are accepted, in this order:
//
//	/dev/sda            a device path, as before v1.1
//	<enclosure>/<slot>  a slot, self-contained
//	<slot>              a slot of the shelf named by scope
//
// scope is the value of --enclosure and is only consulted for the third
// form. A slot reference is never guessed from a device path, so the
// existing device syntax cannot change meaning.
func ParseLEDTarget(value, scope string) (LEDTarget, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return LEDTarget{}, errors.New("empty LED target")
	}
	if strings.HasPrefix(value, "/dev/") {
		if strings.Contains(strings.TrimPrefix(value, "/dev/"), "/") {
			return LEDTarget{}, fmt.Errorf("invalid device %q", value)
		}
		return LEDTarget{Raw: value, Device: value}, nil
	}
	if strings.HasPrefix(value, "/") {
		return LEDTarget{}, fmt.Errorf("invalid target %q: a device must start with /dev/", value)
	}
	if enclosure, slot, ok := strings.Cut(value, "/"); ok {
		if enclosure == "" || slot == "" {
			return LEDTarget{}, fmt.Errorf("invalid target %q: expected <enclosure>/<slot>", value)
		}
		return LEDTarget{Raw: value, Enclosure: enclosure, Slot: slot}, nil
	}
	if scope == "" {
		// A bare word is ambiguous, and the likeliest mistake is a device
		// written without its directory, so the message names both ways out.
		return LEDTarget{}, fmt.Errorf(
			"target %q is neither a device path (/dev/%s) nor an addressable slot: pass --enclosure, or write it as <enclosure>/%s",
			value, value, value)
	}
	return LEDTarget{Raw: scope + "/" + value, Enclosure: scope, Slot: value}, nil
}

// LEDResult is what one LED operation achieved, as opposed to what it
// requested.
type LEDResult struct {
	// Target is the address that was asked for.
	Target string `json:"target"`
	// Enclosure, EnclosureID and Slot are the slot that was written.
	Enclosure   string           `json:"enclosure"`
	EnclosureID Optional[string] `json:"enclosure_id"`
	Slot        string           `json:"slot"`
	// Device is the disk in that slot, when there is one.
	Device Optional[string] `json:"device"`
	Kind   LEDKind          `json:"led"`
	// Requested is the state that was asked for.
	Requested bool `json:"requested"`
	// Observed is what the enclosure reported afterwards. It is absent when
	// the attribute could not be read back at all, which is different from
	// reading back the wrong value.
	Observed Optional[bool] `json:"observed"`
	// Confirmed is true only when a readback showed the requested state.
	// A write that the kernel accepted is not confirmed on its own.
	Confirmed bool `json:"confirmed"`
}

// matches reports whether s is the slot t addresses.
func (t LEDTarget) matches(s Slot, enclosures []Enclosure) bool {
	if t.IsDevice() {
		return s.Device.Or("") == t.Device || s.Map.Or("") == t.Device
	}
	if t.Enclosure != "" && !matchesEnclosure(enclosures, s.Enclosure, t.Enclosure) {
		return false
	}
	if n, err := strconv.ParseInt(t.Slot, 10, 64); err == nil {
		if number, ok := s.Number.Get(); ok && number == n {
			return true
		}
	}
	return strings.EqualFold(t.Slot, s.Label) || strings.EqualFold(t.Slot, s.Name)
}

// matchesEnclosure reports whether the shelf at the given SCSI address
// answers to ref.
func matchesEnclosure(enclosures []Enclosure, address, ref string) bool {
	for _, e := range enclosures {
		if e.Slot == address {
			return e.Matches(ref)
		}
	}
	return strings.EqualFold(address, ref)
}

// SetLED switches one indicator and reports what the enclosure did with it.
//
// The write itself is one byte into a sysfs attribute that already exists;
// the client never creates a file and never touches an attribute it did not
// find (C3). Afterwards the attribute is read back until it reports the
// requested state or the readback budget runs out, because the kernel
// returning success only means the control page was handed to the driver
// (ROADMAP 4).
func (c *Client) SetLED(ctx context.Context, target LEDTarget, kind LEDKind, on bool) (LEDResult, error) {
	if !kind.valid() {
		return LEDResult{}, fmt.Errorf("unknown LED kind %q", string(kind))
	}
	enclosures, err := c.Enclosures(ctx)
	if err != nil {
		return LEDResult{}, err
	}
	if target.Enclosure != "" {
		if _, err := SelectEnclosures(enclosures, target.Enclosure); err != nil {
			return LEDResult{}, err
		}
	}
	slots, err := c.Slots(ctx, enclosures)
	if err != nil {
		return LEDResult{}, err
	}
	// An identifier can match more than one sysfs enclosure: a chassis
	// with two I/O modules exposes the same slot through both, and the
	// module that does not own the bay reports no access to it. Writing
	// through that one would be accepted and do nothing, so an occupied
	// match wins over an unavailable one.
	var chosen *Slot
	for i, s := range slots {
		if !target.matches(s, enclosures) {
			continue
		}
		if chosen == nil || (chosen.Occupancy != OccupancyOccupied && s.Occupancy == OccupancyOccupied) {
			chosen = &slots[i]
		}
	}
	if chosen == nil {
		return LEDResult{}, fmt.Errorf("%w: %s", ErrNoSuchTarget, target.Raw)
	}
	return c.writeLED(ctx, *chosen, target, kind, on)
}

// writeLED performs the write and the readback for one resolved slot.
func (c *Client) writeLED(ctx context.Context, s Slot, target LEDTarget, kind LEDKind, on bool) (LEDResult, error) {
	result := LEDResult{
		Target:      target.Raw,
		Enclosure:   s.Enclosure,
		EnclosureID: s.EnclosureID,
		Slot:        s.Label,
		Device:      s.Device,
		Kind:        kind,
		Requested:   on,
	}
	if n, ok := s.Number.Get(); ok {
		result.Slot = strconv.FormatInt(n, 10)
	}
	path := filepath.Join(c.sysfs, s.Enclosure, s.Name, string(kind))
	if _, err := os.Stat(path); err != nil {
		if gone(err) {
			return result, fmt.Errorf("%w: %s", ErrSlotGone, target.Raw)
		}
		return result, fmt.Errorf("%s does not expose the %s LED", target.Raw, kind)
	}
	value := "0"
	if on {
		value = "1"
	}
	if err := c.ledWriter(path, value); err != nil {
		if gone(err) {
			return result, fmt.Errorf("%w: %s: %w", ErrSlotGone, target.Raw, err)
		}
		return result, err
	}
	result.Observed, result.Confirmed = c.readback(ctx, filepath.Join(c.sysfs, s.Enclosure, s.Name), kind, on)
	if observed, ok := result.Observed.Get(); ok && !result.Confirmed {
		// The shelf answered, and it answered with the other state. This is
		// the case a plain "write succeeded" would have reported as done.
		return result, fmt.Errorf("%w: %s %s reads back as %s after %s",
			ErrLEDNotApplied, target.Raw, kind, onOff(observed), c.ledReadbackTimeout)
	}
	return result, nil
}

// writeAttribute writes one value into an existing sysfs attribute. It never
// creates the file: O_CREATE is deliberately absent.
func writeAttribute(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// readback polls the indicator until it reports want, the budget runs out or
// the context ends. It returns what was last observed and whether it
// matched.
//
// For the fault indicator only the requested bit is compared: ses_set_fault
// sets RQST FAULT and leaves FAULT SENSED to the enclosure, so a shelf that
// is genuinely reporting a fault would otherwise never confirm a clear.
func (c *Client) readback(ctx context.Context, dir string, kind LEDKind, want bool) (Optional[bool], bool) {
	path := filepath.Join(dir, string(kind))
	deadline := time.Now().Add(c.ledReadbackTimeout)
	var last Optional[bool]
	for {
		switch kind {
		case LEDFault:
			last = parseFault(readText(path)).Requested
		default:
			last = From(readBool(path))
		}
		if observed, ok := last.Get(); ok && observed == want {
			return last, true
		}
		if time.Now().After(deadline) {
			return last, false
		}
		select {
		case <-ctx.Done():
			return last, false
		case <-time.After(ledReadbackInterval):
		}
	}
}

// gone reports whether err means the slot is no longer there, as opposed to
// a permission or I/O problem.
func gone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, errENODEV) || errors.Is(err, errENXIO)
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
