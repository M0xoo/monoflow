package initialize

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/antigravity/monoflow/pkg/graph"
)

// InitResult details what was discovered and initialized.
type InitResult struct {
	RepoRoot       string   `json:"repo_root"`
	PackageCount   int      `json:"package_count"`
	DiscoveredPkgs []string `json:"discovered_packages"`
	ConfigPath     string   `json:"config_path"`
}

// InitRepo configures MonoFlow for an existing or new Git repository.
func InitRepo(targetDir string, binarySrcPath string) (*InitResult, error) {
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return nil, err
	}

	// 1. Ensure git repo exists
	gitDir := filepath.Join(absTarget, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		cmd := exec.Command("git", "init")
		cmd.Dir = absTarget
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("failed to git init: %w (output: %s)", err, string(out))
		}
	}

	// 2. Discover packages
	g := graph.New(absTarget)
	_ = g.AutoDiscover()

	allPkgs := g.GetAllPackages()
	pkgNodes := make([]graph.PackageNode, len(allPkgs))
	pkgNames := make([]string, len(allPkgs))
	for i, p := range allPkgs {
		pkgNodes[i] = *p
		pkgNames[i] = p.Name
	}

	// 3. Create .agents directories
	agentsDir := filepath.Join(absTarget, ".agents")
	dirsToCreate := []string{
		agentsDir,
		filepath.Join(agentsDir, "bin"),
		filepath.Join(agentsDir, "rules"),
		filepath.Join(agentsDir, "skills", "monoflow"),
		filepath.Join(agentsDir, "templates"),
		filepath.Join(agentsDir, "worktrees"),
		filepath.Join(agentsDir, "traces"),
		filepath.Join(agentsDir, "changesets"),
	}
	for _, d := range dirsToCreate {
		if err := os.MkdirAll(d, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", d, err)
		}
	}

	// 4. Write .agents/monoflow.config.json
	cfg := graph.Config{
		Version:  "1.0",
		Packages: pkgNodes,
	}
	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(agentsDir, "monoflow.config.json")
	if err := os.WriteFile(configPath, cfgData, 0644); err != nil {
		return nil, err
	}

	// 5. Write Rules and Skills
	rulesPath := filepath.Join(agentsDir, "rules", "agentic-monorepo.md")
	if err := os.WriteFile(rulesPath, []byte(AgentRuleTemplate), 0644); err != nil {
		return nil, err
	}

	// Also mirror into workspace root GEMINI.md or AGENTS.md if not present
	agentsMD := filepath.Join(absTarget, "AGENTS.md")
	if _, err := os.Stat(agentsMD); os.IsNotExist(err) {
		_ = os.WriteFile(agentsMD, []byte(AgentRuleTemplate), 0644)
	}

	skillPath := filepath.Join(agentsDir, "skills", "monoflow", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(AgentSkillTemplate), 0644); err != nil {
		return nil, err
	}

	// 6. Write quarantine.json (empty list)
	quarantinePath := filepath.Join(agentsDir, "quarantine.json")
	if _, err := os.Stat(quarantinePath); os.IsNotExist(err) {
		_ = os.WriteFile(quarantinePath, []byte("[]\n"), 0644)
	}

	// 7. Write task template
	taskTemplatePath := filepath.Join(agentsDir, "templates", "task.json")
	if err := os.WriteFile(taskTemplatePath, []byte(TaskTemplate), 0644); err != nil {
		return nil, err
	}

	// Also drop default .agents/task.json if not present
	activeTaskPath := filepath.Join(agentsDir, "task.json")
	if _, err := os.Stat(activeTaskPath); os.IsNotExist(err) {
		_ = os.WriteFile(activeTaskPath, []byte(TaskTemplate), 0644)
	}

	// 8. Copy binary into .agents/bin/monoflow if binarySrcPath provided
	if binarySrcPath != "" {
		destBinary := filepath.Join(agentsDir, "bin", "monoflow")
		if data, err := os.ReadFile(binarySrcPath); err == nil {
			_ = os.WriteFile(destBinary, data, 0755)
		}
	}

	// 9. Update .gitignore
	gitignorePath := filepath.Join(absTarget, ".gitignore")
	ignoreBlock := "\n# MonoFlow Agent Sandbox & Ephemeral Checkpoints\n.agents/worktrees/\n.agents/traces/\n.agents/checkpoints.json\n"
	if data, err := os.ReadFile(gitignorePath); err == nil {
		if !strings.Contains(string(data), ".agents/worktrees/") {
			_ = os.WriteFile(gitignorePath, append(data, []byte(ignoreBlock)...), 0644)
		}
	} else {
		_ = os.WriteFile(gitignorePath, []byte(ignoreBlock), 0644)
	}

	return &InitResult{
		RepoRoot:       absTarget,
		PackageCount:   len(pkgNodes),
		DiscoveredPkgs: pkgNames,
		ConfigPath:     configPath,
	}, nil
}
