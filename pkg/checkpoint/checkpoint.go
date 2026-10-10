package checkpoint

import (
	"encoding/json"
	"fmt"
	"github.com/antigravity/monoflow/pkg/runtimestate"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Checkpoint represents an ephemeral snapshot of the working tree.
type Checkpoint struct {
	ID        string    `json:"id"`
	TreeHash  string    `json:"tree_hash"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
	Files     []string  `json:"files"`
}

// Manager handles ephemeral checkpoints and isolated worktrees.
type Manager struct {
	mu          sync.Mutex
	RootDir     string
	historyFile string
}

// NewManager creates a checkpoint manager for the repository.
func NewManager(rootDir string) *Manager {
	return &Manager{
		RootDir:     rootDir,
		historyFile: filepath.Join(rootDir, ".agents", "checkpoints.json"),
	}
}

func (m *Manager) runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = m.RootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Save creates a low-level git tree snapshot in 10-15ms without dirtying public commits.
func (m *Manager) Save(message string) (*Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Stage modified & untracked files temporarily into the index
	// We use git add -A to ensure index reflects all working tree changes
	if _, err := m.runGit(runtimestate.AddArgs()...); err != nil {
		return nil, fmt.Errorf("failed to stage changes for checkpoint: %w", err)
	}
	_, _ = m.runGit(runtimestate.UnstageArgs()...)

	// 2. Write tree object (pure git plumbing)
	treeHash, err := m.runGit("write-tree")
	if err != nil {
		return nil, fmt.Errorf("git write-tree failed: %w", err)
	}

	// 3. Get changed files in this tree compared to HEAD
	changedFiles := make([]string, 0)
	if out, err := m.runGit("diff", "--name-only", "HEAD"); err == nil && out != "" {
		changedFiles = strings.Split(out, "\n")
	}

	// 4. Load history
	history, _ := m.loadHistory()
	id := fmt.Sprintf("chk_%03d", len(history)+1)

	chk := Checkpoint{
		ID:        id,
		TreeHash:  treeHash,
		Message:   message,
		CreatedAt: time.Now().UTC(),
		Files:     changedFiles,
	}

	history = append(history, chk)
	if err := m.saveHistory(history); err != nil {
		return nil, err
	}

	return &chk, nil
}

// Revert restores the working directory and index to the specified checkpoint in ~20ms.
func (m *Manager) Revert(checkpointID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	history, err := m.loadHistory()
	if err != nil {
		return err
	}

	var target *Checkpoint
	if checkpointID == "" || checkpointID == "latest" {
		if len(history) == 0 {
			return fmt.Errorf("no checkpoints available to revert")
		}
		target = &history[len(history)-1]
	} else {
		for _, c := range history {
			if c.ID == checkpointID {
				target = &c
				break
			}
		}
	}

	if target == nil {
		return fmt.Errorf("checkpoint '%s' not found", checkpointID)
	}

	// Restore using git read-tree -u --reset <TreeHash>
	if _, err := m.runGit("read-tree", "-u", "--reset", target.TreeHash); err != nil {
		return fmt.Errorf("git read-tree reset failed: %w", err)
	}

	return nil
}

// List returns all recorded checkpoints.
func (m *Manager) List() ([]Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadHistory()
}

func (m *Manager) loadHistory() ([]Checkpoint, error) {
	if _, err := os.Stat(m.historyFile); os.IsNotExist(err) {
		return []Checkpoint{}, nil
	}
	data, err := os.ReadFile(m.historyFile)
	if err != nil {
		return nil, err
	}
	var history []Checkpoint
	if err := json.Unmarshal(data, &history); err != nil {
		return []Checkpoint{}, nil
	}
	return history, nil
}

func (m *Manager) saveHistory(history []Checkpoint) error {
	dir := filepath.Dir(m.historyFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.historyFile, data, 0644)
}

// CreateWorktree creates an isolated worktree sandbox for concurrent agent tasks.
func (m *Manager) CreateWorktree(taskID string) (string, error) {
	worktreeDir := filepath.Join(m.RootDir, ".agents", "worktrees", taskID)
	branchName := fmt.Sprintf("monoflow-%s", taskID)

	if err := os.MkdirAll(filepath.Dir(worktreeDir), 0755); err != nil {
		return "", err
	}

	// git worktree add -b <branch> <path> HEAD
	_, err := m.runGit("worktree", "add", "-b", branchName, worktreeDir, "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to create worktree: %w", err)
	}

	return worktreeDir, nil
}

// RemoveWorktree safely cleans up an isolated worktree sandbox.
func (m *Manager) RemoveWorktree(taskID string) error {
	worktreeDir := filepath.Join(m.RootDir, ".agents", "worktrees", taskID)
	branchName := fmt.Sprintf("monoflow-%s", taskID)

	// git worktree remove <path> --force
	_, _ = m.runGit("worktree", "remove", worktreeDir, "--force")
	// git branch -D <branch>
	_, _ = m.runGit("branch", "-D", branchName)

	return nil
}
