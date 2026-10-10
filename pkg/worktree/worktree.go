package worktree

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	initialize "github.com/antigravity/monoflow/pkg/init"
	"github.com/antigravity/monoflow/pkg/merge"
	"github.com/antigravity/monoflow/pkg/runtimestate"
	"github.com/antigravity/monoflow/pkg/subset"
)

// WorktreeInfo records metadata for an active virtual worktree.
type WorktreeInfo struct {
	AgentID         string    `json:"agent_id"`
	Path            string    `json:"path"`
	CreatedAt       time.Time `json:"created_at"`
	LastHeartbeat   time.Time `json:"last_heartbeat"`
	HeadSHA         string    `json:"head_sha"`
	SymlinksCount   int       `json:"symlinks_count"`
	Isolated        bool      `json:"isolated"`
	IsolationMethod string    `json:"isolation_method,omitempty"`
	ModifiedFiles   []string  `json:"modified_files,omitempty"`
	FocusedPackage  string    `json:"focused_package,omitempty"`
	Packages        []string  `json:"packages,omitempty"`
	OmittedPackages []string  `json:"omitted_packages,omitempty"`
	IsSparse        bool      `json:"is_sparse,omitempty"`
	Strategy        string    `json:"strategy,omitempty"`
	MonoflowBin     string    `json:"monoflow_bin,omitempty"`
	ShieldHooks     bool      `json:"shield_hooks"`
	Branch          string    `json:"branch,omitempty"`
	// Live state (computed by List, not persisted)
	Ahead    int    `json:"ahead,omitempty"`    // commits not yet on the base branch
	Unpushed int    `json:"unpushed,omitempty"` // commits not yet on the remote branch
	PR       string `json:"pr,omitempty"`       // MonoFlow PR id / GitHub URL for this branch
	CI       string `json:"ci,omitempty"`       // latest CI result for this branch (CI_PASSED / CI_FAILED)
}

// CreateOptions configures worktree creation.
type CreateOptions struct {
	IsolatedDeps   bool
	FocusedPackage string
	Packages       []string
	Strategy       subset.DepStrategy
	IncludeDeps    bool
	BaseBranch     string // branch to cut agent/<id> from (default: origin/HEAD, main, master)
}

// IsolateResult details the result of isolating dependencies for an agent worktree.
type IsolateResult struct {
	AgentID         string   `json:"agent_id"`
	IsolatedTargets []string `json:"isolated_targets"`
	DurationMs      float64  `json:"duration_ms"`
	Method          string   `json:"method"`
	Status          string   `json:"status"`
}

// Manager orchestrates high-speed virtual worktrees with shared dependency caches.
type Manager struct {
	mu           sync.Mutex
	RootDir      string
	worktreesDir string
	registryFile string
}

// NewManager creates a worktree manager.
func NewManager(rootDir string) *Manager {
	wtDir := filepath.Join(rootDir, ".agents", "worktrees")
	return &Manager{
		RootDir:      rootDir,
		worktreesDir: wtDir,
		registryFile: filepath.Join(wtDir, "registry.json"),
	}
}

func (m *Manager) runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = m.RootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Create provisions an isolated virtual worktree in ~50ms.
func (m *Manager) Create(agentID string) (*WorktreeInfo, error) {
	return m.CreateWithOpts(agentID, CreateOptions{IsolatedDeps: false})
}

// CreateWithOpts provisions a virtual worktree with optional dependency isolation.
func (m *Manager) CreateWithOpts(agentID string, opts CreateOptions) (*WorktreeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wtPath := filepath.Join(m.worktreesDir, agentID)

	// Clean up existing directory if present
	_ = os.RemoveAll(wtPath)
	_, _ = m.runGit("worktree", "prune")

	if err := os.MkdirAll(m.worktreesDir, 0755); err != nil {
		return nil, err
	}

	// 1. Each agent works on its own branch (agent/<id>) cut from the base branch, never on
	// a detached copy of whatever the shared root happens to have checked out.
	branch := "agent/" + agentID
	if _, err := m.runGit("rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if _, err := m.runGit("worktree", "add", wtPath, branch); err != nil {
			return nil, fmt.Errorf("failed to create git worktree: %w", err)
		}
	} else {
		base := opts.BaseBranch
		if base == "" {
			base = m.defaultBaseBranch()
		}
		if _, err := m.runGit("worktree", "add", "-b", branch, wtPath, base); err != nil {
			return nil, fmt.Errorf("failed to create git worktree: %w", err)
		}
	}

	// 2. Symlink or Clone dependencies (go.work is tracked and handled by subset.SyncGoWork)
	sharedTargets := []string{
		"node_modules",
		".pnpm-store",
		"target",
		".agents/cache",
		".agents/prs",
		".agents/proofs",
		".agents/events",
		".venv",
	}
	for _, d := range sharedStateDirs {
		_ = os.MkdirAll(filepath.Join(m.RootDir, d), 0755)
	}

	// The package cache is content-addressed, so it is always shared with the root repo:
	// agents get cache hits for every untouched dependency. Deliverables in .agents/dist
	// stay per-worktree (restored from that cache) so concurrent agents never overwrite
	// each other's builds of a package they are both modifying.
	_ = os.MkdirAll(filepath.Join(m.RootDir, ".agents", "cache"), 0755)

	symlinkCount := 0
	isolationMethod := ""

	for _, target := range sharedTargets {
		srcPath := filepath.Join(m.RootDir, target)
		destPath := filepath.Join(wtPath, target)

		if _, err := os.Stat(srcPath); err == nil {
			_ = os.MkdirAll(filepath.Dir(destPath), 0755)

			if opts.IsolatedDeps && (target == "node_modules" || target == ".venv") {
				// Clone using Copy-on-Write (CoW / reflink / clonefile)
				method, err := cloneDirectory(srcPath, destPath)
				if err == nil {
					isolationMethod = method
				} else {
					// Fallback to symlink if clone fails
					_ = os.Symlink(srcPath, destPath)
					symlinkCount++
				}
			} else {
				// Fast symlink
				if err := os.Symlink(srcPath, destPath); err == nil {
					symlinkCount++
				}
			}
		}
	}

	// Also ensure .agents configuration is accessible in the worktree
	agentsSrc := filepath.Join(m.RootDir, ".agents")
	agentsDest := filepath.Join(wtPath, ".agents")
	_ = os.MkdirAll(agentsDest, 0755)

	_ = copyFile(filepath.Join(agentsSrc, "monoflow.config.json"), filepath.Join(agentsDest, "monoflow.config.json"))
	_ = copyFile(filepath.Join(agentsSrc, "task.json"), filepath.Join(agentsDest, "task.json"))

	// Propagate local uncommitted environment & config files (.env, .env.local, .env.test)
	// to prevent agents from hallucinating missing environment variables
	envCandidates := []string{".env", ".env.local", ".env.test", ".env.development", "local.config.json"}
	for _, envFile := range envCandidates {
		srcEnv := filepath.Join(m.RootDir, envFile)
		dstEnv := filepath.Join(wtPath, envFile)
		if fileExists(srcEnv) && !fileExists(dstEnv) {
			_ = copyFile(srcEnv, dstEnv)
		}
	}
	if binSrc := filepath.Join(agentsSrc, "bin", "monoflow"); fileExists(binSrc) {
		_ = os.MkdirAll(filepath.Join(agentsDest, "bin"), 0755)
		_ = copyFile(binSrc, filepath.Join(agentsDest, "bin", "monoflow"))
		_ = os.Chmod(filepath.Join(agentsDest, "bin", "monoflow"), 0755)
	}

	// Path symmetry: ./bin/monoflow must work inside the worktree exactly as at the root.
	monoflowBin := m.linkMonoflowBinary(wtPath)
	excludeFromGit(wtPath, append(runtimestate.IgnorePatterns(), sharedDepLinks...)...)

	// Git Shield inheritance: if the root repo is shielded, the worktree must be too.
	if initialize.HasShieldHooks(m.RootDir) && !initialize.HasShieldHooks(wtPath) {
		_, _ = initialize.InstallShieldHooks(wtPath)
	}
	shielded := initialize.HasShieldHooks(wtPath)

	var includedPkgs []string
	var omittedPkgs []string
	isSparse := false

	targetPkgs := opts.Packages
	if opts.FocusedPackage != "" && len(targetPkgs) == 0 {
		targetPkgs = []string{opts.FocusedPackage}
	}

	// Upstream dependencies are always projected unless a strategy is requested
	// explicitly: a sparse worktree missing them cannot build or verify.
	strategy := opts.Strategy
	if strategy == "" {
		strategy = subset.DepStrategySource
	}

	if len(targetPkgs) > 0 {
		subRes, errSub := subset.ApplyWithOptions(wtPath, targetPkgs, strategy)
		if errSub == nil && subRes != nil {
			includedPkgs = subRes.IncludedPackages
			omittedPkgs = subRes.OmittedPackages
			isSparse = true
		}
	}

	// Sparse checkout and hook installation can replace links; re-assert them last.
	m.ensureSharedState(wtPath)

	headSHA, _ := m.runGit("-C", wtPath, "rev-parse", "HEAD")

	info := WorktreeInfo{
		AgentID:         agentID,
		Path:            wtPath,
		CreatedAt:       time.Now().UTC(),
		LastHeartbeat:   time.Now().UTC(),
		HeadSHA:         headSHA,
		SymlinksCount:   symlinkCount,
		Isolated:        opts.IsolatedDeps,
		IsolationMethod: isolationMethod,
		FocusedPackage:  opts.FocusedPackage,
		Packages:        includedPkgs,
		OmittedPackages: omittedPkgs,
		IsSparse:        isSparse,
		Strategy:        string(strategy),
		MonoflowBin:     monoflowBin,
		ShieldHooks:     shielded,
		Branch:          branch,
	}

	registry, _ := m.loadRegistry()
	registry[agentID] = info
	_ = m.saveRegistry(registry)

	return &info, nil
}

// excludeFromGit appends patterns to the worktree's info/exclude (shared by all worktrees).
func excludeFromGit(wtPath string, patterns ...string) {
	pathCmd := exec.Command("git", "rev-parse", "--git-path", "info/exclude")
	pathCmd.Dir = wtPath
	out, err := pathCmd.Output()
	if err != nil {
		return
	}
	excludePath := strings.TrimSpace(string(out))
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(wtPath, excludePath)
	}
	existing, _ := os.ReadFile(excludePath)
	var missing []string
	for _, p := range patterns {
		if !strings.Contains("\n"+string(existing)+"\n", "\n"+p+"\n") {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return
	}
	_ = os.MkdirAll(filepath.Dir(excludePath), 0755)
	if f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		_, _ = f.WriteString("\n# MonoFlow runtime state\n" + strings.Join(missing, "\n") + "\n")
		_ = f.Close()
	}
}

// defaultBaseBranch picks the branch new agent worktrees start from.
func (m *Manager) defaultBaseBranch() string {
	if out, err := m.runGit("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && out != "" {
		if b := strings.TrimPrefix(out, "origin/"); b != "" {
			if _, err := m.runGit("rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
				return b
			}
		}
	}
	for _, b := range []string{"main", "master"} {
		if _, err := m.runGit("rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			return b
		}
	}
	return "HEAD"
}

// resolveMonoflowBinary finds the monoflow binary the root repo uses.
func (m *Manager) resolveMonoflowBinary() string {
	for _, candidate := range []string{
		filepath.Join(m.RootDir, "bin", "monoflow"),
		filepath.Join(m.RootDir, ".agents", "bin", "monoflow"),
	} {
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			if abs, err := filepath.Abs(candidate); err == nil {
				return abs
			}
		}
	}
	if exe, err := os.Executable(); err == nil && filepath.Base(exe) == "monoflow" {
		return exe
	}
	return ""
}

// linkMonoflowBinary provisions <worktree>/bin/monoflow as a symlink to the root binary
// (unless the repo tracks its own bin/monoflow) and keeps it out of git status.
func (m *Manager) linkMonoflowBinary(wtPath string) string {
	dest := filepath.Join(wtPath, "bin", "monoflow")
	if _, err := os.Lstat(dest); err == nil {
		return dest
	}
	src := m.resolveMonoflowBinary()
	if src == "" {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return ""
	}
	if err := os.Symlink(src, dest); err != nil {
		return ""
	}

	check := exec.Command("git", "check-ignore", "-q", "bin/monoflow")
	check.Dir = wtPath
	if check.Run() != nil {
		pathCmd := exec.Command("git", "rev-parse", "--git-path", "info/exclude")
		pathCmd.Dir = wtPath
		if out, err := pathCmd.Output(); err == nil {
			excludePath := strings.TrimSpace(string(out))
			if !filepath.IsAbs(excludePath) {
				excludePath = filepath.Join(wtPath, excludePath)
			}
			_ = os.MkdirAll(filepath.Dir(excludePath), 0755)
			if f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
				_, _ = f.WriteString("\n# MonoFlow worktree binary link\n/bin/monoflow\n")
				_ = f.Close()
			}
		}
	}
	return dest
}

// Isolate promotes an existing worktree to have its own mutable, Copy-on-Write cloned node_modules / venv.
func (m *Manager) Isolate(agentID string) (*IsolateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wtPath := filepath.Join(m.worktreesDir, agentID)
	if !fileExists(wtPath) {
		return nil, fmt.Errorf("worktree for agent '%s' not found", agentID)
	}

	start := time.Now()
	targetsToIsolate := []string{"node_modules", ".venv"}
	var isolated []string
	methodUsed := ""

	for _, t := range targetsToIsolate {
		src := filepath.Join(m.RootDir, t)
		dst := filepath.Join(wtPath, t)

		if fileExists(src) {
			// If dst is currently a symlink, remove it
			if fi, err := os.Lstat(dst); err == nil && (fi.Mode()&os.ModeSymlink != 0) {
				_ = os.Remove(dst)
			}

			method, err := cloneDirectory(src, dst)
			if err == nil {
				isolated = append(isolated, t)
				methodUsed = method
			}
		}
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0

	// Update registry
	reg, _ := m.loadRegistry()
	if info, ok := reg[agentID]; ok {
		info.Isolated = true
		info.IsolationMethod = methodUsed
		reg[agentID] = info
		_ = m.saveRegistry(reg)
	}

	return &IsolateResult{
		AgentID:         agentID,
		IsolatedTargets: isolated,
		DurationMs:      durMs,
		Method:          methodUsed,
		Status:          "isolated",
	}, nil
}

// cloneDirectory uses OS-level Copy-on-Write (APFS clonefile or Linux reflink) to clone directories instantly.
func cloneDirectory(src, dst string) (string, error) {
	_ = os.RemoveAll(dst)

	if runtime.GOOS == "darwin" {
		// macOS APFS clonefile (instant Copy-on-Write)
		cmd := exec.Command("cp", "-c", "-R", src, dst)
		if err := cmd.Run(); err == nil {
			return "apfs-clonefile", nil
		}
	} else if runtime.GOOS == "linux" {
		// Linux reflink (Btrfs, XFS, ZFS)
		cmd := exec.Command("cp", "--reflink=auto", "-R", src, dst)
		if err := cmd.Run(); err == nil {
			return "linux-reflink", nil
		}
	}

	// Fallback recursive copy
	cmd := exec.Command("cp", "-R", src, dst)
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return "copy", nil
}

// List returns all active agent worktrees.
func (m *Manager) List() ([]WorktreeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	reg, err := m.loadRegistry()
	if err != nil {
		return nil, err
	}

	res := make([]WorktreeInfo, 0, len(reg))
	for _, item := range reg {
		// Populate changed files if still active
		itemCopy := item
		if fileExists(item.Path) {
			cmd := exec.Command("git", "status", "--porcelain")
			cmd.Dir = item.Path
			if out, err := cmd.CombinedOutput(); err == nil {
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				var files []string
				for _, l := range lines {
					parts := strings.Fields(l)
					if len(parts) >= 2 {
						files = append(files, parts[len(parts)-1])
					}
				}
				itemCopy.ModifiedFiles = files
			}
			m.fillLiveState(&itemCopy)
		}
		res = append(res, itemCopy)
	}

	return res, nil
}

// Sync merges changes from another agent's worktree into the current worktree using AST synthesis when files collide.
func (m *Manager) Sync(currentAgentID, fromAgentID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	srcDir := filepath.Join(m.worktreesDir, fromAgentID)
	destDir := filepath.Join(m.worktreesDir, currentAgentID)

	if !fileExists(srcDir) {
		return nil, fmt.Errorf("source worktree for agent '%s' not found", fromAgentID)
	}
	if !fileExists(destDir) {
		return nil, fmt.Errorf("destination worktree for agent '%s' not found", currentAgentID)
	}

	// Update heartbeats
	_ = m.touchInternal(currentAgentID)
	_ = m.touchInternal(fromAgentID)

	// Find modified files in srcDir
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = srcDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get git status for %s: %w", fromAgentID, err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var syncedFiles []string

	for _, l := range lines {
		parts := strings.Fields(l)
		if len(parts) >= 2 {
			relPath := parts[len(parts)-1]
			srcFile := filepath.Join(srcDir, relPath)
			destFile := filepath.Join(destDir, relPath)

			if !fileExists(srcFile) {
				continue
			}

			_ = os.MkdirAll(filepath.Dir(destFile), 0755)

			theirsBytes, errTheirs := os.ReadFile(srcFile)
			if errTheirs != nil {
				continue
			}

			if !fileExists(destFile) {
				// File does not exist in destination, simple copy
				_ = os.WriteFile(destFile, theirsBytes, 0644)
				syncedFiles = append(syncedFiles, relPath)
				continue
			}

			// Destination file exists: check if locally modified in destDir
			oursBytes, errOurs := os.ReadFile(destFile)
			if errOurs != nil {
				_ = os.WriteFile(destFile, theirsBytes, 0644)
				syncedFiles = append(syncedFiles, relPath)
				continue
			}

			cmdBase := exec.Command("git", "show", "HEAD:"+relPath)
			cmdBase.Dir = destDir
			baseBytes, _ := cmdBase.Output()

			if bytes.Equal(oursBytes, baseBytes) {
				// Not modified in destination, safe update
				_ = os.WriteFile(destFile, theirsBytes, 0644)
				syncedFiles = append(syncedFiles, relPath)
			} else {
				// Both worktrees modified the same file! Run semantic AST synthesis
				mergeRes, errMerge := merge.Merge3Way(relPath, baseBytes, oursBytes, theirsBytes)
				if errMerge == nil && !mergeRes.HasConflict {
					_ = os.WriteFile(destFile, mergeRes.MergedContent, 0644)
					syncedFiles = append(syncedFiles, relPath+" (synthesized AST)")
				} else {
					// In case of conflict, retain local file safely without writing conflict markers
					syncedFiles = append(syncedFiles, relPath+" (collision: preserved local)")
				}
			}
		}
	}

	return syncedFiles, nil
}

// Touch updates the heartbeat timestamp for an active worktree.
func (m *Manager) Touch(agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.touchInternal(agentID)
}

func (m *Manager) touchInternal(agentID string) error {
	reg, err := m.loadRegistry()
	if err != nil {
		return err
	}
	if info, ok := reg[agentID]; ok {
		info.LastHeartbeat = time.Now().UTC()
		reg[agentID] = info
		return m.saveRegistry(reg)
	}
	return nil
}

// Finish tears down the virtual worktree and prunes git records in ~20ms.
func (m *Manager) Finish(agentID string) error {
	return m.FinishSafe(agentID, true)
}

// FinishSafe removes an agent worktree. Unless force is set it refuses when the worktree
// holds uncommitted changes or unpushed commits. The agent/<id> branch is deleted too
// when nothing on it would be lost.
func (m *Manager) FinishSafe(agentID string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	wtPath := filepath.Join(m.worktreesDir, agentID)
	unsaved := fileExists(wtPath) && hasUnsavedWork(wtPath)
	if unsaved && !force {
		return fmt.Errorf("worktree '%s' has uncommitted changes or unpushed commits; commit and push them (monoflow commit / monoflow push) or pass --force to discard", agentID)
	}
	branch := ""
	if out, err := exec.Command("git", "-C", wtPath, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		branch = strings.TrimSpace(string(out))
	}

	// 1. git worktree remove --force <path>
	_, _ = m.runGit("worktree", "remove", "--force", wtPath)
	_ = os.RemoveAll(wtPath)
	_, _ = m.runGit("worktree", "prune")

	if strings.HasPrefix(branch, "agent/") && !unsaved {
		_, _ = m.runGit("branch", "-D", branch)
	}

	// 2. Remove from registry
	reg, _ := m.loadRegistry()
	delete(reg, agentID)
	_ = m.saveRegistry(reg)

	return nil
}

// ValidAgentID reports whether id is usable as a directory and branch name.
func ValidAgentID(id string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(id)
}

// PruneStale cleans up worktrees that are older than maxAge or orphaned.
func (m *Manager) PruneStale(maxAge time.Duration) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	reg, _ := m.loadRegistry()
	var pruned []string
	now := time.Now().UTC()

	for id, info := range reg {
		lastSeen := lastActivity(info)
		// Never discard work: a worktree with uncommitted changes or unpushed commits is kept.
		if fileExists(info.Path) && hasUnsavedWork(info.Path) {
			continue
		}
		if now.Sub(lastSeen) > maxAge {
			wtPath := filepath.Join(m.worktreesDir, id)
			_, _ = m.runGit("worktree", "remove", "--force", wtPath)
			_ = os.RemoveAll(wtPath)
			delete(reg, id)
			pruned = append(pruned, id)
		}
	}

	// Also clean up any orphaned directory in worktreesDir not tracked in registry
	entries, _ := os.ReadDir(m.worktreesDir)
	for _, e := range entries {
		if e.IsDir() && e.Name() != "registry.json" {
			if _, exists := reg[e.Name()]; !exists {
				orphanPath := filepath.Join(m.worktreesDir, e.Name())
				_, _ = m.runGit("worktree", "remove", "--force", orphanPath)
				_ = os.RemoveAll(orphanPath)
				pruned = append(pruned, e.Name()+" (orphan)")
			}
		}
	}

	_, _ = m.runGit("worktree", "prune")
	_ = m.saveRegistry(reg)

	return pruned, nil
}

// GC is an alias for PruneStale.
func (m *Manager) GC(maxAge time.Duration) ([]string, error) {
	return m.PruneStale(maxAge)
}

func (m *Manager) loadRegistry() (map[string]WorktreeInfo, error) {
	if !fileExists(m.registryFile) {
		return make(map[string]WorktreeInfo), nil
	}
	data, err := os.ReadFile(m.registryFile)
	if err != nil {
		return make(map[string]WorktreeInfo), nil
	}
	var reg map[string]WorktreeInfo
	if err := json.Unmarshal(data, &reg); err != nil {
		return make(map[string]WorktreeInfo), nil
	}
	return reg, nil
}

func (m *Manager) saveRegistry(reg map[string]WorktreeInfo) error {
	_ = os.MkdirAll(m.worktreesDir, 0755)
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.registryFile, data, 0644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// Focus changes an active worktree's focus to a single package (or package subset).
func (m *Manager) Focus(agentID string, targetPackage string, strategy subset.DepStrategy) (*WorktreeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wtPath := filepath.Join(m.worktreesDir, agentID)
	if !fileExists(wtPath) {
		return nil, fmt.Errorf("worktree for agent '%s' not found", agentID)
	}

	subRes, err := subset.ApplyWithOptions(wtPath, []string{targetPackage}, strategy)
	if err != nil {
		return nil, err
	}

	reg, _ := m.loadRegistry()
	info, ok := reg[agentID]
	if !ok {
		info = WorktreeInfo{AgentID: agentID, Path: wtPath}
	}

	info.FocusedPackage = targetPackage
	info.Packages = subRes.IncludedPackages
	info.OmittedPackages = subRes.OmittedPackages
	info.IsSparse = true
	info.Strategy = string(strategy)
	info.LastHeartbeat = time.Now().UTC()

	reg[agentID] = info
	_ = m.saveRegistry(reg)
	return &info, nil
}

// AddPackage adds packages to an active worktree's sparse checkout without wiping existing ones.
func (m *Manager) AddPackage(agentID string, packages []string, strategy subset.DepStrategy) (*WorktreeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wtPath := filepath.Join(m.worktreesDir, agentID)
	if !fileExists(wtPath) {
		return nil, fmt.Errorf("worktree for agent '%s' not found", agentID)
	}

	subRes, err := subset.Add(wtPath, packages, strategy)
	if err != nil {
		return nil, err
	}

	reg, _ := m.loadRegistry()
	info, ok := reg[agentID]
	if !ok {
		info = WorktreeInfo{AgentID: agentID, Path: wtPath}
	}

	info.Packages = subRes.IncludedPackages
	info.OmittedPackages = subRes.OmittedPackages
	info.IsSparse = true
	if strategy != "" {
		info.Strategy = string(strategy)
	}
	info.LastHeartbeat = time.Now().UTC()

	reg[agentID] = info
	_ = m.saveRegistry(reg)
	return &info, nil
}

// RemovePackage removes packages from an active worktree with dependency safety.
func (m *Manager) RemovePackage(agentID string, packages []string, force bool) (*WorktreeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wtPath := filepath.Join(m.worktreesDir, agentID)
	if !fileExists(wtPath) {
		return nil, fmt.Errorf("worktree for agent '%s' not found", agentID)
	}

	subRes, err := subset.Remove(wtPath, packages, force)
	if err != nil {
		return nil, err
	}

	reg, _ := m.loadRegistry()
	info, ok := reg[agentID]
	if !ok {
		info = WorktreeInfo{AgentID: agentID, Path: wtPath}
	}

	info.Packages = subRes.IncludedPackages
	info.OmittedPackages = subRes.OmittedPackages
	info.LastHeartbeat = time.Now().UTC()

	reg[agentID] = info
	_ = m.saveRegistry(reg)
	return &info, nil
}

// UpdateFromRemote pulls selective package updates from a remote branch into the worktree.
func (m *Manager) UpdateFromRemote(agentID string, packages []string, remote, branch string) (*subset.UpdateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wtPath := filepath.Join(m.worktreesDir, agentID)
	if !fileExists(wtPath) {
		return nil, fmt.Errorf("worktree for agent '%s' not found", agentID)
	}

	_ = m.touchInternal(agentID)
	return subset.Update(wtPath, packages, remote, branch)
}

// lastActivity is the most recent sign of life: registry heartbeat, last index write
// (any add/commit/checkout) or last commit in the worktree.
func lastActivity(info WorktreeInfo) time.Time {
	last := info.LastHeartbeat
	if last.IsZero() {
		last = info.CreatedAt
	}
	if out, err := exec.Command("git", "-C", info.Path, "rev-parse", "--git-path", "index").Output(); err == nil {
		p := strings.TrimSpace(string(out))
		if !filepath.IsAbs(p) {
			p = filepath.Join(info.Path, p)
		}
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(last) {
			last = fi.ModTime().UTC()
		}
	}
	if out, err := exec.Command("git", "-C", info.Path, "log", "-1", "--format=%ct").Output(); err == nil {
		if sec, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
			if t := time.Unix(sec, 0).UTC(); t.After(last) {
				last = t
			}
		}
	}
	return last
}

// hasUnsavedWork reports uncommitted changes or commits missing from the remote branch.
func hasUnsavedWork(path string) bool {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:])
		// MonoFlow's own provisioning (config copies, state links, binary link) is not agent work.
		if strings.HasPrefix(p, ".agents/") || p == ".agents" || p == "bin/monoflow" || p == "bin/" || isSharedDepLink(p) {
			continue
		}
		return true
	}
	out, err = exec.Command("git", "-C", path, "rev-list", "--count", "@{upstream}..HEAD").Output()
	if err != nil {
		// No upstream: unsaved if it has commits not contained in any other branch
		// (e.g. the base branch) or remote.
		cur, _ := exec.Command("git", "-C", path, "symbolic-ref", "-q", "HEAD").Output()
		args := []string{"-C", path, "rev-list", "--count", "HEAD", "--not", "--remotes"}
		if refs, err := exec.Command("git", "-C", path, "for-each-ref", "--format=%(refname)", "refs/heads").Output(); err == nil {
			for _, r := range strings.Fields(string(refs)) {
				if r != strings.TrimSpace(string(cur)) {
					args = append(args, r)
				}
			}
		}
		out, err = exec.Command("git", args...).Output()
		if err != nil {
			return true
		}
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n > 0
}

// fillLiveState refreshes branch/HEAD from git and attaches PR and CI status, so one
// `worktree list` call shows where every agent stands.
func (m *Manager) fillLiveState(info *WorktreeInfo) {
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", info.Path}, args...)...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if b := git("rev-parse", "--abbrev-ref", "HEAD"); b != "" {
		info.Branch = b
	}
	if h := git("rev-parse", "HEAD"); h != "" {
		info.HeadSHA = h
	}
	info.LastHeartbeat = lastActivity(*info)
	base := m.defaultBaseBranch()
	info.Ahead, _ = strconv.Atoi(git("rev-list", "--count", base+"..HEAD"))
	if n := git("rev-list", "--count", "@{upstream}..HEAD"); n != "" {
		info.Unpushed, _ = strconv.Atoi(n)
	} else {
		info.Unpushed = info.Ahead
	}

	if entries, err := os.ReadDir(filepath.Join(m.RootDir, ".agents", "prs")); err == nil {
		var newest time.Time
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(m.RootDir, ".agents", "prs", e.Name()))
			if err != nil {
				continue
			}
			var pr struct {
				ID         string    `json:"id"`
				HeadBranch string    `json:"head_branch"`
				GitHubURL  string    `json:"github_url"`
				CreatedAt  time.Time `json:"created_at"`
			}
			if json.Unmarshal(data, &pr) == nil && pr.HeadBranch == info.Branch && pr.CreatedAt.After(newest) {
				newest = pr.CreatedAt
				info.PR = pr.ID
				if pr.GitHubURL != "" {
					info.PR = pr.GitHubURL
				}
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(m.RootDir, ".agents", "events")); err == nil {
		var newest time.Time
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(m.RootDir, ".agents", "events", e.Name()))
			if err != nil {
				continue
			}
			var evt struct {
				Type      string    `json:"type"`
				Branch    string    `json:"branch"`
				CommitSHA string    `json:"commit_sha"`
				CreatedAt time.Time `json:"created_at"`
			}
			if json.Unmarshal(data, &evt) == nil && evt.Branch == info.Branch && strings.HasPrefix(evt.Type, "CI_") && evt.CreatedAt.After(newest) {
				newest = evt.CreatedAt
				info.CI = evt.Type
				if evt.CommitSHA != info.HeadSHA {
					info.CI += " (older commit)"
				}
			}
		}
	}
}

// sharedStateDirs are MonoFlow state directories every worktree shares with the root so
// PRs, proofs, CI events and the package cache are visible from anywhere in the repo.
var sharedStateDirs = []string{".agents/cache", ".agents/prs", ".agents/proofs", ".agents/events"}

// ensureSharedState makes each shared state dir in wtPath a symlink to the root's,
// migrating anything already written to a private copy.
func (m *Manager) ensureSharedState(wtPath string) {
	for _, d := range sharedStateDirs {
		src := filepath.Join(m.RootDir, d)
		dst := filepath.Join(wtPath, d)
		_ = os.MkdirAll(src, 0755)
		if fi, err := os.Lstat(dst); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if fi.IsDir() {
				entries, _ := os.ReadDir(dst)
				for _, e := range entries {
					_ = os.Rename(filepath.Join(dst, e.Name()), filepath.Join(src, e.Name()))
				}
			}
			_ = os.RemoveAll(dst)
		}
		_ = os.MkdirAll(filepath.Dir(dst), 0755)
		_ = os.Symlink(src, dst)
	}
}

// sharedDepLinks are dependency dirs symlinked from the root into worktrees. Typical
// ignore rules ("node_modules/") do not match symlinks, so they are excluded explicitly.
var sharedDepLinks = []string{"/node_modules", "/.pnpm-store", "/target", "/.venv"}

func isSharedDepLink(p string) bool {
	for _, l := range sharedDepLinks {
		if p == strings.TrimPrefix(l, "/") {
			return true
		}
	}
	return false
}
