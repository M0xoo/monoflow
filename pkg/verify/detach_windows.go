//go:build windows

package verify

import "syscall"

func detachedProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }
