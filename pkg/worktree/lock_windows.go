//go:build windows

package worktree

import "os"

func lockFile(f *os.File)   {}
func unlockFile(f *os.File) {}
