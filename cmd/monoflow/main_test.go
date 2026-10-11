package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleSync(t *testing.T) {
	temp := t.TempDir()
	remoteDir := filepath.Join(temp, "remote")
	localDir := filepath.Join(temp, "local")

	_ = os.MkdirAll(remoteDir, 0755)

	runCmd := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s failed: %v (%s)", args, dir, err, string(out))
		}
	}

	// 1. Setup remote repository
	runCmd(remoteDir, "init", "-b", "main")
	runCmd(remoteDir, "config", "user.name", "Remote Test")
	runCmd(remoteDir, "config", "user.email", "remote@test.local")
	_ = os.WriteFile(filepath.Join(remoteDir, "README.md"), []byte("# Remote Repo\n"), 0644)
	runCmd(remoteDir, "add", "README.md")
	runCmd(remoteDir, "commit", "-m", "init remote commit")

	// 2. Clone to local repository
	cmdClone := exec.Command("git", "clone", remoteDir, localDir)
	if out, err := cmdClone.CombinedOutput(); err != nil {
		t.Fatalf("git clone failed: %v (%s)", err, string(out))
	}
	runCmd(localDir, "config", "user.name", "Local Agent")
	runCmd(localDir, "config", "user.email", "agent@local.test")

	// 3. Add a new commit on remote
	_ = os.WriteFile(filepath.Join(remoteDir, "feature.txt"), []byte("new remote feature"), 0644)
	runCmd(remoteDir, "add", "feature.txt")
	runCmd(remoteDir, "commit", "-m", "add remote feature")

	// Get latest remote SHA
	cmdRev := exec.Command("git", "rev-parse", "HEAD")
	cmdRev.Dir = remoteDir
	outRev, err := cmdRev.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse failed: %v", err)
	}
	expectedSHA := string(outRev[:len(outRev)-1])

	// 4. Save and restore os.Args for handleSync
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"monoflow", "sync"}

	// Run handleSync on localDir
	handleSync(localDir)

	// Verify localDir has the new commit
	cmdLocalRev := exec.Command("git", "rev-parse", "HEAD")
	cmdLocalRev.Dir = localDir
	outLocalRev, err := cmdLocalRev.CombinedOutput()
	if err != nil {
		t.Fatalf("local rev-parse failed: %v", err)
	}
	actualSHA := string(outLocalRev[:len(outLocalRev)-1])

	if actualSHA != expectedSHA {
		t.Errorf("expected local SHA %s after sync, got %s", expectedSHA, actualSHA)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(localDir, "feature.txt")); os.IsNotExist(err) {
		t.Errorf("expected feature.txt to exist after sync")
	}
}

func TestHelpFlagsExitZero(t *testing.T) {
	if os.Getenv("MONOFLOW_HELP_CHILD") == "1" {
		os.Args = append([]string{"monoflow"}, strings.Fields(os.Getenv("MONOFLOW_HELP_ARGS"))...)
		main()
		return
	}

	for _, args := range []string{"--help", "-h", "help", "help pr", "pr --help", "worktree -h"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelpFlagsExitZero$")
		cmd.Env = append(os.Environ(), "MONOFLOW_HELP_CHILD=1", "MONOFLOW_HELP_ARGS="+args)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("monoflow %s: expected exit 0, got %v (%s)", args, err, out)
		}
		if strings.Contains(string(out), "Unknown command") || !strings.Contains(string(out), "monoflow") {
			t.Fatalf("monoflow %s: unexpected output: %s", args, out)
		}
	}
}

// Background baseline warm-ups would write into test repos while they are being removed; the
// test that checks them turns them back on.
func TestMain(m *testing.M) {
	_ = os.Setenv("MONOFLOW_NO_WARM", "1")
	os.Exit(m.Run())
}
