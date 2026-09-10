// SPDX-License-Identifier: BSD-2-Clause
package jbod

import "fmt"

// Optional is a value the hardware may not report.
//
// The collectors used to substitute sentinel strings ("ERR", "N/A", "NONE")
// for missing readings, which made a failed sensor indistinguishable from a
// device that really is called NONE and forced the metrics encoder to parse a
// temperature back out of a string. Absence is now part of the type, and the
// sentinels live only in the output layer (C1).
//
// The zero value is absent, so a field nobody filled in reads as absent.
type Optional[T any] struct {
	value   T
	present bool
}

// Some returns an Optional holding v.
func Some[T any](v T) Optional[T] { return Optional[T]{value: v, present: true} }

// None returns an absent Optional.
func None[T any]() Optional[T] { return Optional[T]{} }

// From adapts a parser that returns (value, ok), so a call reads as
// From(serial(path)).
func From[T any](v T, ok bool) Optional[T] {
	if !ok {
		return None[T]()
	}
	return Some(v)
}

// Get returns the value and whether it is present.
func (o Optional[T]) Get() (T, bool) { return o.value, o.present }

// Present reports whether a value was reported.
func (o Optional[T]) Present() bool { return o.present }

// Or returns the value, or fallback when it is absent. Callers that render
// for humans pass their own sentinel here.
func (o Optional[T]) Or(fallback T) T {
	if !o.present {
		return fallback
	}
	return o.value
}

// String makes an absent value obvious in logs and test failures.
func (o Optional[T]) String() string {
	if !o.present {
		return "<none>"
	}
	return fmt.Sprint(o.value)
}
