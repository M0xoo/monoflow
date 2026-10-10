package initialize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "monoflow_init_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a sample multi-package structure
	_ = os.MkdirAll(filepath.Join(tmpDir, "services", "billing"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "services", "billing", "package.json"), []byte(`{"name":"billing","scripts":{"test":"npm test"}}`), 0644)

	_ = os.MkdirAll(filepath.Join(tmpDir, "packages", "shared"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "packages", "shared", "package.json"), []byte(`{"name":"shared"}`), 0644)

	res, err := InitRepo(tmpDir, "")
	if err != nil {
		t.Fatalf("InitRepo failed: %v", err)
	}

	if res.PackageCount != 2 {
		t.Errorf("expected 2 packages discovered, got %d", res.PackageCount)
	}

	// Verify generated files exist
	expectedFiles := []string{
		filepath.Join(tmpDir, ".agents", "monoflow.config.json"),
		filepath.Join(tmpDir, ".agents", "rules", "agentic-monorepo.md"),
		filepath.Join(tmpDir, ".agents", "skills", "monoflow", "SKILL.md"),
		filepath.Join(tmpDir, ".agents", "quarantine.json"),
		filepath.Join(tmpDir, ".agents", "templates", "task.json"),
		filepath.Join(tmpDir, ".agents", "task.json"),
		filepath.Join(tmpDir, ".gitignore"),
		filepath.Join(tmpDir, "AGENTS.md"),
	}

	for _, ef := range expectedFiles {
		if _, err := os.Stat(ef); os.IsNotExist(err) {
			t.Errorf("missing expected generated file: %s", ef)
		}
	}
}

func TestMergeSectionKeepsProjectInstructions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	_ = os.WriteFile(path, []byte("# Project rules\n\nUse tabs.\n"), 0644)
	for i := 0; i < 2; i++ { // idempotent
		if err := mergeSection(path, AgentRuleTemplate, true); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.HasPrefix(s, "# Project rules\n\nUse tabs.\n") {
		t.Fatalf("existing instructions must be preserved, got:\n%s", s[:80])
	}
	if strings.Count(s, sectionStart) != 1 || !strings.Contains(s, "monoflow verify --affected") {
		t.Fatalf("expected exactly one MonoFlow section")
	}
	missing := filepath.Join(t.TempDir(), "CLAUDE.md")
	_ = mergeSection(missing, ClaudePointer, false)
	if _, err := os.Stat(missing); err == nil {
		t.Fatalf("CLAUDE.md must not be created when absent")
	}
}
