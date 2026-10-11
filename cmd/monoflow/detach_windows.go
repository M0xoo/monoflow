//go:build windows

package main

import "syscall"

// detachedSysProcAttr lets a background child (ship --deliver) outlive the command.
func detachedSysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }
