package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antigravity/monoflow/pkg/checkpoint"
	"github.com/antigravity/monoflow/pkg/events"
	"github.com/antigravity/monoflow/pkg/graph"
)

func setupTestGitRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "monoflow-outer-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v (out: %s)", strings.Join(args, " "), err, string(out))
		}
	}

	run("init", "-b", "main")
	run("config", "user.name", "Test Agent")
	run("config", "user.email", "agent@monoflow.dev")

	// Create initial file & commit
	initialFile := filepath.Join(dir, "README.md")
	_ = os.WriteFile(initialFile, []byte("# MonoFlow Repo\n"), 0644)
	run("add", "README.md")
	run("commit", "-m", "initial commit")

	return dir
}

func TestOuterLoopSuccess(t *testing.T) {
	repoDir := setupTestGitRepo(t)
	defer os.RemoveAll(repoDir)

	// Create a dummy package with passing test
	pkgDir := filepath.Join(repoDir, "pkg", "auth")
	_ = os.MkdirAll(pkgDir, 0755)
	_ = os.WriteFile(filepath.Join(pkgDir, "auth.go"), []byte("package auth\n"), 0644)

	// Configure mock dependency graph
	g := graph.New(repoDir)
	g.AddPackage(&graph.PackageNode{
		Name:    "pkg/auth",
		Path:    "pkg/auth",
		TestCmd: "exit 0",
	})
	_ = g.SaveConfig(filepath.Join(repoDir, ".agents", "monoflow.config.json"))

	// Create a commit
	cmd := exec.Command("git", "add", "pkg/auth")
	cmd.Dir = repoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "feat: auth module")
	cmd.Dir = repoDir
	_ = cmd.Run()

	om := NewOuterLoopManager(repoDir)
	job, err := om.StartJob(OuterLoopOptions{
		AutoPush:   false,
		AutoRevert: false,
		Async:      false,
	})
	if err != nil {
		t.Fatalf("StartJob failed: %v", err)
	}

	if job.Status != JobPassed {
		t.Fatalf("expected job status 'passed', got '%s' (error: %s)", job.Status, job.ErrorSummary)
	}

	if job.PassedPackages != 1 {
		t.Errorf("expected 1 passed package, got %d", job.PassedPackages)
	}

	if job.ReceiptPath == "" {
		t.Errorf("expected generated receipt path, got empty")
	}

	// Verify job can be loaded
	loaded, err := om.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if loaded.Status != JobPassed {
		t.Errorf("loaded job status mismatch: %s", loaded.Status)
	}
}

func TestOuterLoopRegressionBlocksRemote(t *testing.T) {
	repoDir := setupTestGitRepo(t)
	defer os.RemoveAll(repoDir)

	// Create a failing package
	pkgDir := filepath.Join(repoDir, "pkg", "broken")
	_ = os.MkdirAll(pkgDir, 0755)
	_ = os.WriteFile(filepath.Join(pkgDir, "broken.go"), []byte("package broken\n"), 0644)

	g := graph.New(repoDir)
	g.AddPackage(&graph.PackageNode{
		Name:    "pkg/broken",
		Path:    "pkg/broken",
		TestCmd: "exit 1", // Simulates failing test
	})
	_ = g.SaveConfig(filepath.Join(repoDir, ".agents", "monoflow.config.json"))

	cmd := exec.Command("git", "add", "pkg/broken")
	cmd.Dir = repoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "feat: broken module")
	cmd.Dir = repoDir
	_ = cmd.Run()

	om := NewOuterLoopManager(repoDir)
	job, err := om.StartJob(OuterLoopOptions{
		AutoPush:   true, // AutoPush enabled, but MUST be blocked!
		AutoRevert: false,
		Async:      false,
	})

	if err == nil {
		t.Fatalf("expected error on failing test suite, got nil")
	}

	if job.Status != JobRegressed {
		t.Fatalf("expected job status 'regressed', got '%s'", job.Status)
	}

	// VERIFY STRICT INVARIANT: Must NEVER push to remote on regression
	if job.Promoted {
		t.Fatalf("CRITICAL SECURITY VIOLATION: regressed job was marked as promoted to remote!")
	}

	if job.AlertPath == "" {
		t.Errorf("expected regression alert event to be created")
	}

	if _, err := os.Stat(job.AlertPath); os.IsNotExist(err) {
		t.Errorf("alert file does not exist at %s", job.AlertPath)
	}
}

func TestOuterLoopAsyncExecution(t *testing.T) {
	repoDir := setupTestGitRepo(t)
	defer os.RemoveAll(repoDir)

	pkgDir := filepath.Join(repoDir, "pkg", "async_pkg")
	_ = os.MkdirAll(pkgDir, 0755)
	_ = os.WriteFile(filepath.Join(pkgDir, "code.go"), []byte("package async_pkg\n"), 0644)

	g := graph.New(repoDir)
	g.AddPackage(&graph.PackageNode{
		Name:    "pkg/async_pkg",
		Path:    "pkg/async_pkg",
		TestCmd: "sleep 0.1 && exit 0",
	})
	_ = g.SaveConfig(filepath.Join(repoDir, ".agents", "monoflow.config.json"))

	cmd := exec.Command("git", "add", "pkg/async_pkg")
	cmd.Dir = repoDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "feat: async pkg")
	cmd.Dir = repoDir
	_ = cmd.Run()

	om := NewOuterLoopManager(repoDir)
	job, err := om.StartJob(OuterLoopOptions{
		AutoPush: false,
		Async:    true, // Run in background!
	})
	if err != nil {
		t.Fatalf("StartJob async failed: %v", err)
	}

	if job.Status != JobQueued && job.Status != JobRunning && job.Status != JobPassed {
		t.Fatalf("unexpected async initial status: %s", job.Status)
	}

	// Wait for async completion
	finishedJob, err := om.WaitJob(job.ID, 3*time.Second)
	if err != nil {
		t.Fatalf("WaitJob failed: %v", err)
	}

	if finishedJob.Status != JobPassed {
		t.Fatalf("expected finished status 'passed', got '%s'", finishedJob.Status)
	}
}

func TestOuterLoopRegressionEmitsEventAndAutoReverts(t *testing.T) {
	repoDir := setupTestGitRepo(t)
	defer os.RemoveAll(repoDir)

	pkgDir := filepath.Join(repoDir, "pkg", "broken")
	_ = os.MkdirAll(pkgDir, 0755)
	_ = os.WriteFile(filepath.Join(pkgDir, "broken.go"), []byte("package broken\n"), 0644)

	g := graph.New(repoDir)
	g.AddPackage(&graph.PackageNode{Name: "pkg/broken", Path: "pkg/broken", TestCmd: "exit 1"})
	_ = g.SaveConfig(filepath.Join(repoDir, ".agents", "monoflow.config.json"))

	for _, args := range [][]string{{"add", "pkg/broken"}, {"commit", "-m", "feat: broken module"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		_ = cmd.Run()
	}

	// A green checkpoint the auto-revert can fall back to.
	if _, err := checkpoint.NewManager(repoDir).Save("last green"); err != nil {
		t.Fatalf("failed to save checkpoint: %v", err)
	}

	om := NewOuterLoopManager(repoDir)
	job, err := om.StartJob(OuterLoopOptions{AutoRevert: true})
	if err == nil {
		t.Fatalf("expected regression error, got nil")
	}
	if job.Status != JobRolledBack || !job.RollbackTriggered {
		t.Fatalf("expected job rolled back, got status=%s rollback=%v", job.Status, job.RollbackTriggered)
	}

	unread, err := events.NewEventManager(repoDir).List(true)
	if err != nil {
		t.Fatalf("failed to list events: %v", err)
	}
	var evt *events.Event
	for _, e := range unread {
		if e.Type == events.EventOuterRegression {
			evt = e
		}
	}
	if evt == nil {
		t.Fatalf("expected OUTER_LOOP_REGRESSION event on the bus, got %d unread events", len(unread))
	}
	if !evt.RequiresAction || !strings.Contains(evt.SuggestedTask, "pkg/broken") {
		t.Errorf("expected actionable suggested_task naming pkg/broken, got %q", evt.SuggestedTask)
	}
	if evt.Payload["rollback_triggered"] != true {
		t.Errorf("expected payload rollback_triggered=true, got %v", evt.Payload["rollback_triggered"])
	}
	if job.AlertPath != filepath.Join(repoDir, ".agents", "events", evt.ID+".json") {
		t.Errorf("expected AlertPath to point at the emitted event, got %s", job.AlertPath)
	}
}
