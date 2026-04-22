//go:build unix

package main

import "syscall"

// sysProcAttrDetached configures the child to start in a new session,
// fully detaching it from the parent's controlling terminal.
func sysProcAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
