//go:build !windows

package conflict

import "syscall"

func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
