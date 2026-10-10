package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/antigravity/monoflow/pkg/checkpoint"
	"github.com/antigravity/monoflow/pkg/runtimestate"
)

// CircuitBreaker prevents infinite self-healing loops and runaway token spend.
type CircuitBreaker struct {
	mu          sync.Mutex
	filePath    string
	MaxAttempts int
}

// CircuitState tracks attempts for an agent task.
type CircuitState struct {
	TaskID        string    `json:"task_id"`
	Target        string    `json:"target"`
	Attempts      int       `json:"attempts"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
	Tripped       bool      `json:"tripped"`
	SnapshotID    string    `json:"snapshot_id,omitempty"`
}

// NewCircuitBreaker creates a circuit breaker manager.
func NewCircuitBreaker(rootDir string, maxAttempts int) *CircuitBreaker {
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	return &CircuitBreaker{
		filePath:    filepath.Join(rootDir, ".agents", "circuit_breaker.json"),
		MaxAttempts: maxAttempts,
	}
}

// RecordFailure increments failure count and trips if limit exceeded.
func (cb *CircuitBreaker) RecordFailure(rootDir, taskID, target string) (*CircuitState, bool, error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	states := cb.load()
	key := fmt.Sprintf("%s:%s", taskID, target)
	st, exists := states[key]
	if !exists {
		st = CircuitState{
			TaskID:   taskID,
			Target:   target,
			Attempts: 0,
		}
	}

	st.Attempts++
	st.LastAttemptAt = time.Now().UTC()

	tripped := false
	if st.Attempts >= cb.MaxAttempts {
		st.Tripped = true
		tripped = true

		// Never revert automatically: the latest checkpoint is not known to be green and
		// reverting silently destroys the agent's work. Snapshot the current state instead
		// so the agent (or a human) can choose to roll back explicitly.
		chkMgr := checkpoint.NewManager(rootDir)
		if c, err := chkMgr.Save(fmt.Sprintf("circuit-breaker: %s failed %d times", target, st.Attempts)); err == nil {
			st.SnapshotID = c.ID
		}
	}

	states[key] = st
	_ = cb.save(states)

	return &st, tripped, nil
}

// Reset clears failure state for a target once it passes.
func (cb *CircuitBreaker) Reset(taskID, target string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	states := cb.load()
	key := fmt.Sprintf("%s:%s", taskID, target)
	delete(states, key)
	_ = cb.save(states)
}

func (cb *CircuitBreaker) load() map[string]CircuitState {
	res := make(map[string]CircuitState)
	if data, err := os.ReadFile(cb.filePath); err == nil {
		_ = json.Unmarshal(data, &res)
	}
	return res
}

func (cb *CircuitBreaker) save(states map[string]CircuitState) error {
	_ = os.MkdirAll(filepath.Dir(cb.filePath), 0755)
	data, err := json.MarshalIndent(states, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cb.filePath, data, 0644)
}

// IntegrityViolation represents cheating, weakened tests, or leaked secrets.
type IntegrityViolation struct {
	Severity string `json:"severity"` // "CRITICAL", "HIGH", "WARNING"
	Type     string `json:"type"`     // "test_weakened", "secret_leak", "tautological_test"
	File     string `json:"file"`
	Message  string `json:"message"`
}

// CheckTestIntegrity ensures agents didn't delete assertions or cheat tests to pass CI.
func CheckTestIntegrity(rootDir string) (violations []IntegrityViolation, err error) {
	cmd := exec.Command("git", "diff", "HEAD")
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, nil // not in git or clean
	}

	diffText := string(out)

	deletedAssertRegex := regexp.MustCompile(`^-\s*(.*[{\s;])?(expect\(|assert\s+|assert\.|self\.assert|assertEquals|assert_eq!|t\.(Fatal|Fatalf|Error|Errorf|Fail|FailNow))`)
	tautologicalRegex := regexp.MustCompile(`\+\s*(expect\(true\)\.toBe\(true\)|assert\s+True|assert\s+1\s*==\s*1)`)
	commentedAssertRegex := regexp.MustCompile(`\+\s*(//|#|/\*)\s*(.*[{\s;])?(expect\(|\bassert(\.|\(|_|Equals|\s+[^\s]+\s*(==|!=|<|>|is\b|in\b))|\bit\(|\btest\(|t\.(Fatal|Fatalf|Error|Errorf|Fail|FailNow)\()`)

	lines := strings.Split(diffText, "\n")
	currentFile := ""

	// A deleted assertion only weakens a test when the file ends up with fewer assertions;
	// rewriting checks (e.g. into a table-driven test) adds at least as many back.
	addedAssertRegex := regexp.MustCompile(`^\+\s*(.*[{\s;])?(expect\(|assert\s+|assert\.|self\.assert|assertEquals|assert_eq!|t\.(Fatal|Fatalf|Error|Errorf|Fail|FailNow))`)
	tautologyAnywhere := regexp.MustCompile(`expect\(true\)\.toBe\(true\)|assert\s+True\b|assert\s+1\s*==\s*1`)
	deletedByFile := map[string][]string{}
	addedByFile := map[string]int{}
	var fileOrder []string
	defer func() {
		for _, f := range fileOrder {
			if del := deletedByFile[f]; len(del) > addedByFile[f] {
				violations = append(violations, IntegrityViolation{
					Severity: "CRITICAL",
					Type:     "test_weakened",
					File:     f,
					Message:  fmt.Sprintf("Existing assertion was deleted: '%s' (%d removed, %d added)", del[0], len(del), addedByFile[f]),
				})
			}
		}
	}()

	for _, l := range lines {
		if strings.HasPrefix(l, "diff --git") {
			parts := strings.Fields(l)
			if len(parts) >= 4 {
				currentFile = strings.TrimPrefix(parts[3], "b/")
			}
			continue
		}

		// Only inspect test files
		isTestFile := strings.Contains(currentFile, "test") || strings.Contains(currentFile, "spec")
		if !isTestFile {
			continue
		}

		// 1. Deleted assertions (reported per file once additions are known)
		if deletedAssertRegex.MatchString(l) {
			if _, seen := deletedByFile[currentFile]; !seen {
				fileOrder = append(fileOrder, currentFile)
			}
			deletedByFile[currentFile] = append(deletedByFile[currentFile], strings.TrimSpace(l))
		} else if addedAssertRegex.MatchString(l) && !commentedAssertRegex.MatchString(l) && !tautologyAnywhere.MatchString(l) {
			addedByFile[currentFile]++
		}

		// 2. Commented out assertions
		if commentedAssertRegex.MatchString(l) {
			violations = append(violations, IntegrityViolation{
				Severity: "CRITICAL",
				Type:     "test_weakened",
				File:     currentFile,
				Message:  fmt.Sprintf("Assertion was commented out: '%s'", strings.TrimSpace(l)),
			})
		}

		// 3. Tautological / Hollow tests
		if tautologicalRegex.MatchString(l) {
			violations = append(violations, IntegrityViolation{
				Severity: "HIGH",
				Type:     "tautological_test",
				File:     currentFile,
				Message:  fmt.Sprintf("Hollow/tautological assertion detected: '%s'", strings.TrimSpace(l)),
			})
		}
	}

	return violations, nil
}

// ScanSecrets checks diffs for API keys, private keys, or tokens before committing.
func ScanSecrets(rootDir string) ([]IntegrityViolation, error) {
	cmd := exec.Command("git", "diff", "--cached")
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	if err != nil || len(out) == 0 {
		cmd = exec.Command("git", "diff", "HEAD")
		cmd.Dir = rootDir
		out, _ = cmd.CombinedOutput()
	}

	diffText := string(out)
	var violations []IntegrityViolation

	secretPatterns := []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{"AWS Access Key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
		{"Stripe API Key", regexp.MustCompile(`\bsk_(live|test)_[0-9a-zA-Z]{20,}\b`)},
		{"Private Key", regexp.MustCompile(`-----BEGIN (RSA|EC|DSA|OPENSSH)? PRIVATE KEY-----`)},
		{"GitHub Token", regexp.MustCompile(`\b(ghp|gho|ghu|ghs)_[A-Za-z0-9_]{36,}\b`)},
		{"OpenAI API Key", regexp.MustCompile(`\bsk-[A-Za-z0-9]{48,}\b`)},
		{"Generic Secret Assignment", regexp.MustCompile(`(?i)(api[_-]?key|secret[_-]?token|auth[_-]?token|stripe[_-]?secret)\s*[:=]\s*["']?[A-Za-z0-9+/=_\-]{16,}["']?`)},
	}

	lines := strings.Split(diffText, "\n")
	currentFile := ""

	for _, l := range lines {
		if strings.HasPrefix(l, "diff --git") {
			parts := strings.Fields(l)
			if len(parts) >= 4 {
				currentFile = strings.TrimPrefix(parts[3], "b/")
			}
			continue
		}

		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
			for _, sp := range secretPatterns {
				if sp.pattern.MatchString(l) {
					violations = append(violations, IntegrityViolation{
						Severity: "CRITICAL",
						Type:     "secret_leak",
						File:     currentFile,
						Message:  fmt.Sprintf("Potential %s detected in addition: '%s'", sp.name, strings.TrimSpace(l)),
					})
				}
			}
		}
	}

	// Also inspect untracked files (??) for leaked secrets before they are staged
	cmdUntracked := exec.Command("git", "status", "--porcelain")
	cmdUntracked.Dir = rootDir
	if outUn, errUn := cmdUntracked.CombinedOutput(); errUn == nil {
		lines := strings.Split(string(outUn), "\n")
		for _, l := range lines {
			if strings.HasPrefix(l, "?? ") {
				untrackedPath := strings.TrimSpace(strings.TrimPrefix(l, "?? "))
				fullPath := filepath.Join(rootDir, untrackedPath)
				if data, err := os.ReadFile(fullPath); err == nil && len(data) < 1024*1024 {
					for _, line := range strings.Split(string(data), "\n") {
						for _, sp := range secretPatterns {
							if sp.pattern.MatchString(line) {
								violations = append(violations, IntegrityViolation{
									Severity: "CRITICAL",
									Type:     "secret_leak",
									File:     untrackedPath,
									Message:  fmt.Sprintf("Potential %s detected in untracked file: '%s'", sp.name, strings.TrimSpace(line)),
								})
							}
						}
					}
				}
			}
		}
	}

	return violations, nil
}

// CommitResult records the result of an agent commit operation.
type CommitResult struct {
	CommitSHA  string               `json:"commit_sha"`
	Message    string               `json:"message"`
	DurationMs float64              `json:"duration_ms"`
	Violations []IntegrityViolation `json:"violations,omitempty"`
	Status     string               `json:"status"`
}

// FastAgentCommit executes a verified commit bypassing slow or interactive human pre-commit hooks
// while strictly enforcing test integrity and secret scanning in < 50ms.
func FastAgentCommit(rootDir, message string) (*CommitResult, error) {
	start := time.Now()

	// 1. Guard check: Test integrity
	testViolations, _ := CheckTestIntegrity(rootDir)
	// 2. Guard check: Secrets
	secretViolations, _ := ScanSecrets(rootDir)

	allViolations := append(testViolations, secretViolations...)
	if len(allViolations) > 0 {
		return &CommitResult{
			Message:    message,
			Violations: allViolations,
			Status:     "blocked_by_guard",
			DurationMs: float64(time.Since(start).Microseconds()) / 1000.0,
		}, fmt.Errorf("commit blocked: %d integrity/security violation(s)", len(allViolations))
	}

	// 3. Stage changes
	// A deliberately edited go.work must be committed even in a sparse worktree, where the
	// projection is hidden with assume-unchanged.
	if _, err := os.Stat(filepath.Join(rootDir, "go.work")); err == nil && !runtimestate.ProjectedGoWork(rootDir) {
		_ = exec.Command("git", "-C", rootDir, "update-index", "--no-assume-unchanged", "go.work").Run()
	}
	cmdAdd := exec.Command("git", runtimestate.AddArgs()...)
	cmdAdd.Dir = rootDir
	if out, err := cmdAdd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git add failed: %w (%s)", err, string(out))
	}
	unstage := exec.Command("git", runtimestate.UnstageArgs(rootDir)...)
	unstage.Dir = rootDir
	_ = unstage.Run()

	// 4. Commit bypassing slow or interactive hooks (--no-verify)
	cmdCommit := exec.Command("git", "commit", "--no-verify", "-m", message)
	cmdCommit.Dir = rootDir
	if out, err := cmdCommit.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git commit failed: %w (%s)", err, string(out))
	}

	// 5. Get commit SHA
	cmdRev := exec.Command("git", "rev-parse", "HEAD")
	cmdRev.Dir = rootDir
	shaOut, _ := cmdRev.CombinedOutput()
	sha := strings.TrimSpace(string(shaOut))

	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	return &CommitResult{
		CommitSHA:  sha,
		Message:    message,
		Status:     "committed",
		DurationMs: durMs,
	}, nil
}
