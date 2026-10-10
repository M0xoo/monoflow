package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TaskBundle defines the boundary contracts and acceptance criteria for an agent task.
type TaskBundle struct {
	TaskID             string   `json:"task_id"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	AllowedPaths       []string `json:"allowed_paths"`   // e.g. ["services/billing/**"]
	ReadOnlyPaths      []string `json:"read_only_paths"` // e.g. ["contracts/**"]
	ContextFiles       []string `json:"context_files"`
}

// BoundaryResult records whether file modifications complied with task boundaries.
type BoundaryResult struct {
	Valid      bool     `json:"valid"`
	Violations []string `json:"violations,omitempty"`
}

// IsInternalPath reports whether a path is MonoFlow-generated metadata or VCS plumbing
// (.agents/**, .git/**, .gitignore) rather than agent-authored source. The task contract
// itself (.agents/task.json) is never internal, so agents cannot silently widen their scope.
func IsInternalPath(path string) bool {
	clean := strings.TrimPrefix(strings.ReplaceAll(filepath.Clean(path), "\\", "/"), "./")
	clean = strings.TrimPrefix(clean, "/")
	if clean == ".agents/task.json" {
		return false
	}
	return clean == ".gitignore" || clean == ".git" || clean == ".agents" ||
		strings.HasPrefix(clean, ".git/") || strings.HasPrefix(clean, ".agents/")
}

// CheckBoundaries verifies that modified files do not violate AllowedPaths or ReadOnlyPaths.
// Internal MonoFlow artifacts are exempt from AllowedPaths but explicit ReadOnlyPaths still apply.
func CheckBoundaries(bundle *TaskBundle, modifiedFiles []string) BoundaryResult {
	var violations []string

	for _, file := range modifiedFiles {
		clean := filepath.Clean(file)
		clean = strings.ReplaceAll(clean, "\\", "/")
		clean = strings.TrimPrefix(clean, "/")
		internal := IsInternalPath(clean)

		// Check ReadOnlyPaths
		for _, ro := range bundle.ReadOnlyPaths {
			if matchPattern(ro, clean) {
				violations = append(violations, fmt.Sprintf("VIOLATION: '%s' is strictly read-only for this task", clean))
			}
		}

		// Check AllowedPaths (if defined)
		if len(bundle.AllowedPaths) > 0 && !internal {
			allowed := false
			for _, allow := range bundle.AllowedPaths {
				if matchPattern(allow, clean) {
					allowed = true
					break
				}
			}
			if !allowed {
				violations = append(violations, fmt.Sprintf("VIOLATION: '%s' is outside declared allowed_paths", clean))
			}
		}
	}

	return BoundaryResult{
		Valid:      len(violations) == 0,
		Violations: violations,
	}
}

// matchPattern supports simple glob matching like 'services/billing/**' or '*.ts'.
func matchPattern(pattern, path string) bool {
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	pattern = strings.TrimPrefix(pattern, "/")

	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}

	if strings.HasPrefix(pattern, "*.") {
		ext := strings.TrimPrefix(pattern, "*")
		return strings.HasSuffix(path, ext)
	}

	matched, _ := filepath.Match(pattern, path)
	return matched || pattern == path
}

// StackedSlice categorizes files into clean stacked PR diff tiers.
type StackedSlice struct {
	TypesAndContracts   []string `json:"01_types_and_contracts"`
	CoreImplementation  []string `json:"02_core_implementation"`
	TestsAndConsumers   []string `json:"03_tests_and_consumers"`
}

// SliceStackedDiff categorizes a list of modified files into 3 stacked review tiers.
func SliceStackedDiff(files []string) StackedSlice {
	var types []string
	var impl []string
	var tests []string

	for _, f := range files {
		clean := strings.ToLower(f)
		if strings.Contains(clean, ".proto") || strings.Contains(clean, "types.") || strings.Contains(clean, "schema.") || strings.Contains(clean, "contracts/") {
			types = append(types, f)
		} else if strings.Contains(clean, "_test.") || strings.Contains(clean, ".test.") || strings.Contains(clean, ".spec.") || strings.Contains(clean, "tests/") {
			tests = append(tests, f)
		} else {
			impl = append(impl, f)
		}
	}

	return StackedSlice{
		TypesAndContracts:  types,
		CoreImplementation: impl,
		TestsAndConsumers:  tests,
	}
}

// Changeset records package version bump intent for CI automation.
type Changeset struct {
	Package   string    `json:"package"`
	BumpType  string    `json:"bump_type"` // "patch", "minor", "major"
	Summary   string    `json:"summary"`
	TaskID    string    `json:"task_id"`
	CreatedAt time.Time `json:"created_at"`
}

// SaveChangeset writes a changeset file to .agents/changesets/<id>.json.
func SaveChangeset(rootDir string, cs Changeset) error {
	dir := filepath.Join(rootDir, ".agents", "changesets")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	filename := fmt.Sprintf("%s-%s.json", cs.TaskID, strings.ReplaceAll(cs.Package, "/", "_"))
	data, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, filename), data, 0644)
}
