// SPDX-License-Identifier: BSD-2-Clause

package jbod

import "syscall"

// The errnos a sysfs attribute returns once its device is gone. They are
// named here so led.go can tell a pulled drive from a permission problem
// without importing syscall all over the package.
var (
	errENODEV error = syscall.ENODEV
	errENXIO  error = syscall.ENXIO
)
