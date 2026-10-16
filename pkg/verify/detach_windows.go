//go:build windows

package verify

import "syscall"

func detachedProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }

func lockFileBlocking(string) func() { return func() {} }
