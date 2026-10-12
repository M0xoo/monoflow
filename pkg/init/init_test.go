package initialize

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antigravity/monoflow/pkg/graph"
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
	if !strings.HasPrefix(s, sectionStart) || !strings.HasSuffix(s, "# Project rules\n\nUse tabs.\n") {
		t.Fatalf("MonoFlow section first, existing instructions preserved after it, got:\n%s", s)
	}
	if strings.Count(s, sectionStart) != 1 || !strings.Contains(s, "monoflow verify") {
		t.Fatalf("expected exactly one MonoFlow section")
	}
}

// Claude Code loads CLAUDE.md, not AGENTS.md: init puts the full MonoFlow guide in both,
// creating CLAUDE.md when missing and keeping an existing one's instructions.
func TestInitWritesGuideToClaudeMD(t *testing.T) {
	for _, existing := range []string{"", "# Our rules\n\nNo force pushes.\n"} {
		dir := t.TempDir()
		_ = exec.Command("git", "-C", dir, "init", "-q").Run()
		if existing != "" {
			_ = os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(existing), 0644)
		}
		for i := 0; i < 2; i++ { // idempotent
			if _, err := InitRepo(dir, ""); err != nil {
				t.Fatal(err)
			}
		}
		data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
		if err != nil {
			t.Fatalf("CLAUDE.md must exist after init (existing=%q): %v", existing, err)
		}
		s := string(data)
		if strings.Count(s, sectionStart) != 1 || !strings.Contains(s, "monoflow verify") || !strings.Contains(s, "monoflow ship") {
			t.Fatalf("CLAUDE.md must carry the MonoFlow guide once, got:\n%s", s)
		}
		if existing != "" && !strings.HasSuffix(s, existing) {
			t.Fatalf("CLAUDE.md must keep the project's instructions, got:\n%s", s)
		}
	}
}

func TestInstallAgentHooksEventDelivery(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 2; i++ { // idempotent
		if err := InstallAgentHooks(root); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	s := string(data)
	for _, ev := range []string{"PreToolUse", "SessionStart", "UserPromptSubmit", "PostToolUse", "Stop"} {
		if !strings.Contains(s, `"`+ev+`"`) {
			t.Errorf("missing %s hook", ev)
		}
	}
	if strings.Count(s, "hook stop") != 1 || strings.Count(s, "hook deliver") != 3 {
		t.Errorf("hooks must be installed exactly once:\n%s", s)
	}
	plugin, _ := os.ReadFile(filepath.Join(root, ".opencode", "plugin", "monoflow.js"))
	if !strings.Contains(string(plugin), "session.idle") || !strings.Contains(string(plugin), "client.session.prompt") {
		t.Errorf("opencode plugin must deliver events to idle sessions")
	}
}

// Re-running init keeps the commands the repo configured per package (even a blanked one) and
// fills in only what the old config lacks.
func TestInitKeepsConfiguredCommands(t *testing.T) {
	dir := t.TempDir()
	_ = exec.Command("git", "-C", dir, "init", "-q").Run()
	_ = os.MkdirAll(filepath.Join(dir, "svc"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "svc", "go.mod"), []byte("module example.com/svc\n\ngo 1.21\n"), 0644)
	if _, err := InitRepo(dir, ""); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, ".agents", "monoflow.config.json")
	var cfg graph.Config
	data, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(data, &cfg)
	if len(cfg.Packages) != 1 || cfg.Packages[0].FixCmd != "gofmt -w {files:.go}" {
		t.Fatalf("discovered fix_cmd: %+v", cfg.Packages)
	}
	cfg.Packages[0].TestCmd = "make test-svc"
	cfg.Packages[0].LintCmd = ""
	cfg.Packages[0].FixCmd = "none"
	cfg.VerifyExclude = []string{"tools/"}
	data, _ = json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath, data, 0644)

	if _, err := InitRepo(dir, ""); err != nil {
		t.Fatal(err)
	}
	var again graph.Config
	data, _ = os.ReadFile(cfgPath)
	_ = json.Unmarshal(data, &again)
	p := again.Packages[0]
	if p.TestCmd != "make test-svc" || p.LintCmd != "" || p.FixCmd != "none" || len(again.VerifyExclude) != 1 || again.VerifyExclude[0] != "tools/" {
		t.Fatalf("init must keep configured commands: %+v exclude=%v", p, again.VerifyExclude)
	}
}
