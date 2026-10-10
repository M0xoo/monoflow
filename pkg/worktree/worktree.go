package worktree

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// WorktreeInfo records metadata for an active virtual worktree.
type WorktreeInfo struct {
	AgentID         string    `json:"agent_id"`
	Path            string    `json:"path"`
	CreatedAt       time.Time `json:"created_at"`
	HeadSHA         string    `json:"head_sha"`
	SymlinksCount   int       `json:"symlinks_count"`
	Isolated        bool      `json:"isolated"`
	IsolationMethod string    `json:"isolation_method,omitempty"`
	ModifiedFiles   []string  `json:"modified_files,omitempty"`
}

// CreateOptions configures worktree creation.
type CreateOptions struct {
	IsolatedDeps bool
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

	// 1. git worktree add --detach <path> HEAD
	_, err := m.runGit("worktree", "add", "--detach", wtPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to create git worktree: %w", err)
	}

	// 2. Symlink or Clone dependencies
	sharedTargets := []string{
		"node_modules",
		".pnpm-store",
		"target",
		".agents/cache",
		"go.work",
		".venv",
	}

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
	if binSrc := filepath.Join(agentsSrc, "bin", "monoflow"); fileExists(binSrc) {
		_ = os.MkdirAll(filepath.Join(agentsDest, "bin"), 0755)
		_ = copyFile(binSrc, filepath.Join(agentsDest, "bin", "monoflow"))
		_ = os.Chmod(filepath.Join(agentsDest, "bin", "monoflow"), 0755)
	}

	headSHA, _ := m.runGit("rev-parse", "HEAD")

	info := WorktreeInfo{
		AgentID:         agentID,
		Path:            wtPath,
		CreatedAt:       time.Now().UTC(),
		HeadSHA:         headSHA,
		SymlinksCount:   symlinkCount,
		Isolated:        opts.IsolatedDeps,
		IsolationMethod: isolationMethod,
	}

	registry, _ := m.loadRegistry()
	registry[agentID] = info
	_ = m.saveRegistry(registry)

	return &info, nil
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
		}
		res = append(res, itemCopy)
	}

	return res, nil
}

// Sync merges changes from another agent's worktree into the current worktree.
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

			if fileExists(srcFile) {
				_ = os.MkdirAll(filepath.Dir(destFile), 0755)
				if data, err := os.ReadFile(srcFile); err == nil {
					_ = os.WriteFile(destFile, data, 0644)
					syncedFiles = append(syncedFiles, relPath)
				}
			}
		}
	}

	return syncedFiles, nil
}

// Finish tears down the virtual worktree and prunes git records in ~20ms.
func (m *Manager) Finish(agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	wtPath := filepath.Join(m.worktreesDir, agentID)

	// 1. git worktree remove --force <path>
	_, _ = m.runGit("worktree", "remove", "--force", wtPath)
	_ = os.RemoveAll(wtPath)
	_, _ = m.runGit("worktree", "prune")

	// 2. Remove from registry
	reg, _ := m.loadRegistry()
	delete(reg, agentID)
	_ = m.saveRegistry(reg)

	return nil
}

// PruneStale cleans up worktrees that are older than maxAge.
func (m *Manager) PruneStale(maxAge time.Duration) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	reg, _ := m.loadRegistry()
	var pruned []string
	now := time.Now().UTC()

	for id, info := range reg {
		if now.Sub(info.CreatedAt) > maxAge {
			wtPath := filepath.Join(m.worktreesDir, id)
			_, _ = m.runGit("worktree", "remove", "--force", wtPath)
			_ = os.RemoveAll(wtPath)
			delete(reg, id)
			pruned = append(pruned, id)
		}
	}

	_, _ = m.runGit("worktree", "prune")
	_ = m.saveRegistry(reg)

	return pruned, nil
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
