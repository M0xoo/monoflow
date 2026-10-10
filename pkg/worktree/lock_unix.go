//go:build !windows

package worktree

import (
	"os"
	"syscall"
)

func lockFile(f *os.File)   { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
func unlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
