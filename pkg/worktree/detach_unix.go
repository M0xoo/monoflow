//go:build !windows

package worktree

import "syscall"

func detachedSysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
