package jbod

import "testing"

func TestOptional(t *testing.T) {
	t.Parallel()
	// The zero value is absent, so a field nobody filled in is not an empty
	// string that has to be told apart from a real reading.
	var zero Optional[string]
	if zero.Present() {
		t.Error("zero value reported as present")
	}
	if got := zero.Or("NONE"); got != "NONE" {
		t.Errorf("Or on absent = %q, want the fallback", got)
	}
	if got := zero.String(); got != "<none>" {
		t.Errorf("String on absent = %q", got)
	}
	// An empty string is a value like any other.
	empty := Some("")
	if !empty.Present() {
		t.Error("Some(\"\") reported as absent")
	}
	if got := empty.Or("NONE"); got != "" {
		t.Errorf("Or on Some(\"\") = %q, want the value", got)
	}
	if v, ok := Some(int64(-2)).Get(); !ok || v != -2 {
		t.Errorf("Get = %d, %v", v, ok)
	}
	if None[int64]().Present() {
		t.Error("None reported as present")
	}
	// From adapts the (value, ok) parsers.
	if got := From("S123", true); got.Or("") != "S123" {
		t.Errorf("From(value, true) = %v", got)
	}
	if From("leftover", false).Present() {
		t.Error("From(value, false) kept the value")
	}
	if got := Some(37).String(); got != "37" {
		t.Errorf("String = %q", got)
	}
}
