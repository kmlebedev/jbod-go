// SPDX-License-Identifier: BSD-2-Clause

package jbod

import "strings"

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
