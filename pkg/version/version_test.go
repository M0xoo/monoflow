package version

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/antigravity/monoflow/pkg/task"
)

func setupVersionTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, string(out))
		}
	}

	run("init", "-b", "main")
	run("config", "user.name", "Version Test Agent")
	run("config", "user.email", "agent@version.test")

	// Create 2 packages: core and api (api depends on core)
	_ = os.MkdirAll(filepath.Join(dir, "packages", "core"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "packages", "api"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, ".agents", "changesets"), 0755)

	_ = os.WriteFile(filepath.Join(dir, "packages", "core", "package.json"), []byte(`{
  "name": "@monoflow/core",
  "version": "1.0.0"
}`), 0644)

	_ = os.WriteFile(filepath.Join(dir, "packages", "api", "package.json"), []byte(`{
  "name": "@monoflow/api",
  "version": "1.0.0",
  "dependencies": {
    "@monoflow/core": "^1.0.0"
  }
}`), 0644)

	configJSON := `{
  "version": "1.0",
  "packages": [
    {
      "name": "core",
      "path": "packages/core",
      "language": "typescript",
      "version": "1.0.0",
      "dependencies": []
    },
    {
      "name": "api",
      "path": "packages/api",
      "language": "typescript",
      "version": "1.0.0",
      "dependencies": ["core"]
    }
  ]
}`
	_ = os.WriteFile(filepath.Join(dir, ".agents", "monoflow.config.json"), []byte(configJSON), 0644)

	run("add", "-A")
	run("commit", "-m", "init version test repo")

	return dir
}

func TestSemVerParsingAndBumping(t *testing.T) {
	sv, err := ParseSemVer("v1.2.3")
	if err != nil {
		t.Fatalf("ParseSemVer failed: %v", err)
	}
	if sv.Major != 1 || sv.Minor != 2 || sv.Patch != 3 {
		t.Fatalf("unexpected semver parsed: %+v", sv)
	}

	// Bump patch
	if err := sv.Bump("patch"); err != nil {
		t.Fatal(err)
	}
	if sv.String() != "1.2.4" {
		t.Errorf("expected 1.2.4, got %s", sv.String())
	}

	// Bump minor
	if err := sv.Bump("minor"); err != nil {
		t.Fatal(err)
	}
	if sv.String() != "1.3.0" {
		t.Errorf("expected 1.3.0, got %s", sv.String())
	}

	// Bump major
	if err := sv.Bump("major"); err != nil {
		t.Fatal(err)
	}
	if sv.String() != "2.0.0" {
		t.Errorf("expected 2.0.0, got %s", sv.String())
	}
}

func TestListPackageVersions(t *testing.T) {
	repo := setupVersionTestRepo(t)

	report, err := ListPackageVersions(repo)
	if err != nil {
		t.Fatalf("ListPackageVersions failed: %v", err)
	}

	if report.MonoFlowVersion != EngineVersion {
		t.Errorf("expected engine version %s, got %s", EngineVersion, report.MonoFlowVersion)
	}
	if report.TotalPackages != 2 {
		t.Fatalf("expected 2 packages, got %d", report.TotalPackages)
	}

	var foundCore, foundAPI bool
	for _, p := range report.Packages {
		if p.Name == "core" && p.Version == "1.0.0" {
			foundCore = true
		}
		if p.Name == "api" && p.Version == "1.0.0" {
			foundAPI = true
		}
	}
	if !foundCore || !foundAPI {
		t.Errorf("expected core and api with version 1.0.0 in report")
	}
}

func TestBumpPackageVersionAndCascade(t *testing.T) {
	repo := setupVersionTestRepo(t)

	// Bump core with minor bump and cascade
	res, err := BumpPackageVersion(repo, "core", "minor", true)
	if err != nil {
		t.Fatalf("BumpPackageVersion failed: %v", err)
	}

	if res.OldVersion != "1.0.0" || res.NewVersion != "1.1.0" {
		t.Errorf("expected bump 1.0.0 -> 1.1.0, got %s -> %s", res.OldVersion, res.NewVersion)
	}
	if len(res.CascadedDependents) != 1 || res.CascadedDependents[0] != "api" {
		t.Errorf("expected api to be cascaded, got: %v", res.CascadedDependents)
	}

	// Check core package.json
	corePkgData, _ := os.ReadFile(filepath.Join(repo, "packages", "core", "package.json"))
	if !containsString(string(corePkgData), `"version": "1.1.0"`) {
		t.Errorf("expected core package.json to have version 1.1.0, got: %s", string(corePkgData))
	}

	// Check api package.json for cascaded dependency update
	apiPkgData, _ := os.ReadFile(filepath.Join(repo, "packages", "api", "package.json"))
	if !containsString(string(apiPkgData), `"^1.1.0"`) {
		t.Errorf("expected api package.json to have dependency ^1.1.0, got: %s", string(apiPkgData))
	}
}

func TestApplyChangesets(t *testing.T) {
	repo := setupVersionTestRepo(t)

	// Save two changesets for core: one patch and one minor
	_ = task.SaveChangeset(repo, task.Changeset{
		Package:   "core",
		BumpType:  "patch",
		Summary:   "fix bug in core",
		TaskID:    "task-01",
		CreatedAt: time.Now().UTC(),
	})
	_ = task.SaveChangeset(repo, task.Changeset{
		Package:   "core",
		BumpType:  "minor",
		Summary:   "add new API to core",
		TaskID:    "task-02",
		CreatedAt: time.Now().UTC(),
	})

	// Apply changesets (minor should win over patch)
	res, err := ApplyChangesets(repo)
	if err != nil {
		t.Fatalf("ApplyChangesets failed: %v", err)
	}

	if res.AppliedChangesets != 2 {
		t.Errorf("expected 2 applied changesets, got %d", res.AppliedChangesets)
	}
	if len(res.BumpedPackages) != 1 {
		t.Fatalf("expected 1 bumped package, got %d", len(res.BumpedPackages))
	}
	if res.BumpedPackages[0].NewVersion != "1.1.0" {
		t.Errorf("expected core new version 1.1.0, got %s", res.BumpedPackages[0].NewVersion)
	}

	// Check changesets are archived
	appliedEntries, _ := os.ReadDir(filepath.Join(repo, ".agents", "changesets", "applied"))
	if len(appliedEntries) != 2 {
		t.Errorf("expected 2 archived changesets in applied folder, got %d", len(appliedEntries))
	}
}

func TestCheckVersionConsistencyWithSubsets(t *testing.T) {
	repo := setupVersionTestRepo(t)

	// In full repo: should be consistent
	report1, err := CheckVersionConsistency(repo)
	if err != nil {
		t.Fatalf("CheckVersionConsistency failed: %v", err)
	}
	if report1.Status != "consistent" {
		t.Errorf("expected consistent, got %s", report1.Status)
	}

	// Now simulate subset where ONLY 'api' is checked out, but 'core' is omitted
	subsetJSON := `{
  "requested_packages": ["api"],
  "included_packages": ["api"],
  "omitted_packages": ["core"],
  "strategy": "none"
}`
	_ = os.WriteFile(filepath.Join(repo, ".agents", "subset.json"), []byte(subsetJSON), 0644)

	// Check consistency: should warn that active package 'api' depends on omitted package 'core'
	report2, err := CheckVersionConsistency(repo)
	if err != nil {
		t.Fatalf("CheckVersionConsistency failed: %v", err)
	}
	if !report2.IsSubsetActive {
		t.Errorf("expected IsSubsetActive=true")
	}
	if report2.Status != "warning" {
		t.Errorf("expected warning status for omitted dependency, got %s", report2.Status)
	}
	if len(report2.Violations) == 0 {
		t.Errorf("expected violation for omitted dependency core")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && stringIndex(s, substr) >= 0)
}

func stringIndex(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
