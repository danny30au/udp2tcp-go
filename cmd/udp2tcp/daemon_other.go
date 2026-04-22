//go:build !unix

package main

import "syscall"

// sysProcAttrDetached is a no-op on non-Unix platforms; daemon mode is
// best-effort there and primarily intended for Linux/OpenWrt deployments.
func sysProcAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
