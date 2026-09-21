// SPDX-License-Identifier: BSD-2-Clause

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kmlebedev/jbod-go/internal/jbod"
)

// targets collects a repeatable --locate/--fault option. It keeps the raw
// strings and defers interpretation to ParseLEDTarget, because a bare slot
// number only makes sense once --enclosure has been parsed too.
type targets []string

// String renders the collected targets for the usage message.
func (t *targets) String() string { return strings.Join(*t, ",") }

// Set appends one target. The only thing rejected this early is an empty
// value; whether a target resolves is a question for the inventory.
func (t *targets) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("empty target")
	}
	*t = append(*t, v)
	return nil
}

// Type names the value in the usage message.
func (t *targets) Type() string { return "target" }

// tuner is implemented by *jbod.Client. The led command needs a client with
// its own readback budget, and Inventory deliberately does not expose
// construction; a type assertion keeps the fakes in the tests simple while
// the real client still honours the flag.
type tuner interface {
	With(opts ...jbod.Option) *jbod.Client
}

func cmdLED(ctx context.Context, args []string, out io.Writer, inv Inventory) error {
	f := flags("led", out)
	f.SetNormalizeFunc(enclosureAliases)
	var locate, fault targets
	f.VarP(&locate, "locate", "l", "target whose locate LED to switch")
	f.VarP(&fault, "fault", "f", "target whose fault LED to switch")
	scope := f.String("enclosure-id", "", "shelf a bare slot target belongs to")
	on := f.Bool("on", false, "turn the LED on")
	off := f.Bool("off", false, "turn the LED off")
	readback := f.Duration("readback-timeout", jbod.DefaultLEDReadbackTimeout,
		"how long to wait for the enclosure to report the requested state")
	asJSON := f.Bool("json", false, "print the results as JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("led takes no arguments, got %q", f.Arg(0))
	}
	if *on == *off || len(locate)+len(fault) == 0 {
		return errors.New("led requires target(s) and exactly one of --on or --off")
	}
	if *readback < 0 {
		return fmt.Errorf("invalid readback timeout %s", *readback)
	}
	// Resolve every target before touching the hardware, so a typo in the
	// third one does not leave the first two switched.
	type request struct {
		target jbod.LEDTarget
		kind   jbod.LEDKind
	}
	var requests []request
	for _, group := range []struct {
		kind  jbod.LEDKind
		names targets
	}{{jbod.LEDLocate, locate}, {jbod.LEDFault, fault}} {
		for _, name := range group.names {
			target, err := jbod.ParseLEDTarget(name, *scope)
			if err != nil {
				return err
			}
			requests = append(requests, request{target: target, kind: group.kind})
		}
	}
	if err := inv.Preflight(); err != nil {
		return err
	}
	if client, ok := inv.(tuner); ok {
		inv = client.With(jbod.WithLEDReadbackTimeout(*readback))
	}
	// Writes happen in order and stop at the first failure; earlier ones
	// are not rolled back, which is why the results collected so far are
	// still printed.
	var document ledDocument
	var failure error
	for _, r := range requests {
		result, err := inv.SetLED(ctx, r.target, r.kind, *on)
		if result.Target != "" {
			document.Results = append(document.Results, result)
		}
		if err != nil {
			failure = err
			break
		}
	}
	if *asJSON {
		document.Results = emptyToSlice(document.Results)
		if err := writeJSON(out, document); err != nil {
			return err
		}
		return failure
	}
	for _, result := range document.Results {
		fmt.Fprintln(out, ledLine(result))
	}
	return failure
}

// ledLine renders one result.
//
// The confirmation is part of the line on purpose: the point of the
// readback is that "the write returned success" and "the indicator is on"
// are different claims, and a command that printed only the first would be
// making the second (ROADMAP 4).
func ledLine(r jbod.LEDResult) string {
	state := "off"
	if r.Requested {
		state = "on"
	}
	// What the target resolved to is part of the line: an identifier can
	// match two sysfs enclosures of the same chassis, and a device path
	// does not say which bay it is in.
	var resolved []string
	if r.Enclosure != "" {
		resolved = append(resolved, r.Enclosure+" slot "+r.Slot)
	}
	if device, ok := r.Device.Get(); ok && device != r.Target {
		resolved = append(resolved, device)
	}
	where := ""
	if len(resolved) > 0 {
		where = " [" + strings.Join(resolved, ", ") + "]"
	}
	switch {
	case r.Confirmed:
		return fmt.Sprintf("%s %s: %s (confirmed)%s", r.Target, r.Kind, state, where)
	case r.Observed.Present():
		return fmt.Sprintf("%s %s: %s (NOT confirmed: reads back as %s)%s",
			r.Target, r.Kind, state, onOff(r.Observed.Or(false)), where)
	default:
		return fmt.Sprintf("%s %s: %s (write accepted, no readback available)%s", r.Target, r.Kind, state, where)
	}
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
