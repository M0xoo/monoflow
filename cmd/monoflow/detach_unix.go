//go:build !windows

package main

import "syscall"

// detachedSysProcAttr lets a background child (ship --deliver) outlive the command.
func detachedSysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// killGroup stops a detached child and everything it started (it leads its own session).
func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGTERM) }

// backgroundNice prefixes background verify runs to give them low CPU priority.
const backgroundNice = "nice -n 15 "

// lowerPriority gives the current process (and what it starts) low CPU priority.
func lowerPriority() { _ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 15) }
