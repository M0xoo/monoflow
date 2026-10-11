//go:build windows

package main

import (
	"os"
	"syscall"
)

// detachedSysProcAttr lets a background child (ship --deliver) outlive the command.
func detachedSysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }

func killGroup(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
