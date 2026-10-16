//go:build !windows

package verify

import (
	"os"
	"syscall"
)

func detachedProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// lockFileBlocking takes an exclusive flock on path, waiting for its holder, and returns its
// release. The lock goes away with the process, so a killed run never leaves it held.
func lockFileBlocking(path string) func() {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX) != nil {
		_ = f.Close()
		return func() {}
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
}
