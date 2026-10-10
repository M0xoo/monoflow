package initialize

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/agenthooks"
	"github.com/antigravity/monoflow/pkg/runtimestate"
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

	// Agent instructions: merge a delimited MonoFlow section into AGENTS.md (keeping any
	// existing project instructions) and point CLAUDE.md at it when the repo has one.
	_ = mergeSection(filepath.Join(absTarget, "AGENTS.md"), AgentRuleTemplate, true)
	_ = mergeSection(filepath.Join(absTarget, "CLAUDE.md"), ClaudePointer, false)

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
	ignoreBlock := "\n# MonoFlow runtime state (never committed)\n" + strings.Join(runtimestate.IgnorePatterns(), "\n") + "\n"
	if data, err := os.ReadFile(gitignorePath); err == nil {
		if !strings.Contains(string(data), "/.agents/cache") {
			_ = os.WriteFile(gitignorePath, append(data, []byte(ignoreBlock)...), 0644)
		}
	} else {
		_ = os.WriteFile(gitignorePath, []byte(ignoreBlock), 0644)
	}

	// 9b. Agent harness hooks: redirect raw build/test/git commands to MonoFlow.
	_ = InstallAgentHooks(absTarget)

	// 10. Install Git Shield Hooks into the repository's effective hooks directory
	if _, err := InstallShieldHooks(absTarget); err != nil {
		return nil, fmt.Errorf("failed to install git shield hooks: %w", err)
	}

	// 11. Install Git Interception Shim into .agents/bin/git
	gitShimPath := filepath.Join(agentsDir, "bin", "git")
	gitShim := `#!/bin/sh
# MonoFlow Git Interception Shim
case "$1" in
    push)
        shift
        exec ./bin/monoflow push "$@"
        ;;
    commit)
        shift
        exec ./bin/monoflow commit "$@"
        ;;
    *)
        exec /usr/bin/git "$@"
        ;;
esac
`
	_ = os.WriteFile(gitShimPath, []byte(gitShim), 0755)

	return &InitResult{
		RepoRoot:       absTarget,
		PackageCount:   len(pkgNodes),
		DiscoveredPkgs: pkgNames,
		ConfigPath:     configPath,
	}, nil
}

// shieldMarker identifies hook scripts written by MonoFlow.
const shieldMarker = "MonoFlow Shield"

const prePushHook = `#!/bin/sh
# MonoFlow Shield: Intercept direct git push
if [ "$MONOFLOW_INTERNAL" != "1" ]; then
    echo "🛑 MONOFLOW SHIELD: Direct 'git push' is disabled in this monorepo!" >&2
    echo "👉 Please use: ./bin/monoflow push" >&2
    echo "   (Guarantees Protected Branch Shield, pre-push race detection, and async CI watcher)" >&2
    exit 1
fi
`

const preCommitHook = `#!/bin/sh
# MonoFlow Shield: Intercept direct git commit
if [ "$MONOFLOW_INTERNAL" != "1" ]; then
    echo "🛑 MONOFLOW SHIELD: Direct 'git commit' is disabled in this monorepo!" >&2
    echo "👉 Please use: ./bin/monoflow commit -m \"...\"" >&2
    echo "   (Guarantees sub-50ms test integrity checks and secret scanning)" >&2
    exit 1
fi
`

// HooksDir resolves the hooks directory git actually uses for dir. This honours
// core.hooksPath and linked worktrees (where .git is a file, not a directory).
func HooksDir(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-path", "hooks")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %s", dir)
	}
	hooks := strings.TrimSpace(string(out))
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(dir, hooks)
	}
	return hooks, nil
}

// InstallShieldHooks writes the MonoFlow pre-commit and pre-push shield hooks into the
// effective hooks directory of dir and returns that directory.
func InstallShieldHooks(dir string) (string, error) {
	hooksDir, err := HooksDir(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return "", err
	}
	for name, body := range map[string]string{"pre-push": prePushHook, "pre-commit": preCommitHook} {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(body), 0755); err != nil {
			return "", err
		}
	}
	return hooksDir, nil
}

// HasShieldHooks reports whether git operations in dir are guarded by the MonoFlow shield.
func HasShieldHooks(dir string) bool {
	hooksDir, err := HooksDir(dir)
	if err != nil {
		return false
	}
	for _, name := range []string{"pre-commit", "pre-push"} {
		data, err := os.ReadFile(filepath.Join(hooksDir, name))
		if err != nil || !strings.Contains(string(data), shieldMarker) {
			return false
		}
	}
	return true
}

// InstallAgentHooks installs harness-level hooks (Claude Code PreToolUse in
// .claude/settings.json, an opencode plugin) that deny raw build/test/git commands and
// name the MonoFlow replacement. Existing Claude settings are merged, never overwritten.
func InstallAgentHooks(repoRoot string) error {
	settingsPath := filepath.Join(repoRoot, ".claude", "settings.json")
	settings := map[string]any{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("cannot merge into %s: %w", settingsPath, err)
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	pre, _ := hooks["PreToolUse"].([]any)
	installed := false
	for _, h := range pre {
		if b, _ := json.Marshal(h); strings.Contains(string(b), "hook pretooluse") {
			installed = true
		}
	}
	if !installed {
		hooks["PreToolUse"] = append(pre, agenthooks.ClaudeSettingsHook())
		settings["hooks"] = hooks
		data, _ := json.MarshalIndent(settings, "", "  ")
		_ = os.MkdirAll(filepath.Dir(settingsPath), 0755)
		if err := os.WriteFile(settingsPath, append(data, '\n'), 0644); err != nil {
			return err
		}
	}

	pluginPath := filepath.Join(repoRoot, ".opencode", "plugin", "monoflow.js")
	if _, err := os.Stat(pluginPath); os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Dir(pluginPath), 0755)
		return os.WriteFile(pluginPath, []byte(agenthooks.OpencodePlugin), 0644)
	}
	return nil
}

const (
	sectionStart = "<!-- monoflow:start (managed by monoflow init) -->"
	sectionEnd   = "<!-- monoflow:end -->"
)

// ClaudePointer is merged into an existing CLAUDE.md (Claude Code reads CLAUDE.md, not AGENTS.md).
const ClaudePointer = `## MonoFlow
This monorepo is governed by MonoFlow; follow the MonoFlow section of AGENTS.md.
Use ` + "`.agents/bin/monoflow verify --affected`" + ` for checks and ` + "`monoflow commit` / `monoflow pr create`" + ` instead of raw git/gh.
`

// mergeSection inserts or replaces the MonoFlow-managed section of a markdown file,
// leaving everything else untouched. When create is false a missing file is left absent.
func mergeSection(path, content string, create bool) error {
	block := sectionStart + "\n" + strings.TrimSpace(content) + "\n" + sectionEnd + "\n"
	data, err := os.ReadFile(path)
	if err != nil {
		if !create {
			return nil
		}
		return os.WriteFile(path, []byte(block), 0644)
	}
	s := string(data)
	if i := strings.Index(s, sectionStart); i >= 0 {
		if j := strings.Index(s[i:], sectionEnd); j >= 0 {
			end := i + j + len(sectionEnd)
			if end < len(s) && s[end] == '\n' {
				end++
			}
			return os.WriteFile(path, []byte(s[:i]+block+s[end:]), 0644)
		}
	}
	// Legacy installs wrote the bare template as the whole file: replace it.
	if strings.HasPrefix(strings.TrimSpace(s), "# MonoFlow") {
		return os.WriteFile(path, []byte(block), 0644)
	}
	return os.WriteFile(path, []byte(strings.TrimRight(s, "\n")+"\n\n"+block), 0644)
}
