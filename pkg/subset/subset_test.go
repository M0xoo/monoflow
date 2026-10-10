package subset

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antigravity/monoflow/pkg/graph"
)

func setupTestMonorepo(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "monoflow_subset_test_*")
	if err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, string(out))
		}
	}

	run("init", "-b", "main")
	run("config", "user.name", "MonoFlow Test")
	run("config", "user.email", "test@monoflow.dev")

	// Create 4 packages:
	// 1. packages/proto (dependency of contracts)
	// 2. packages/contracts (depends on proto)
	// 3. services/billing (depends on contracts)
	// 4. apps/web-frontend (unrelated package)
	dirs := []string{
		"packages/proto",
		"packages/contracts",
		"services/billing",
		"apps/web-frontend",
		".agents",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(tmpDir, d), 0755); err != nil {
			t.Fatal(err)
		}
	}

	_ = os.WriteFile(filepath.Join(tmpDir, "packages/proto", "schema.proto"), []byte("syntax = 'proto3';\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "packages/contracts", "index.ts"), []byte("export const v = 1;\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "services/billing", "service.go"), []byte("package billing\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "apps/web-frontend", "index.html"), []byte("<h1>Web</h1>\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module monorepo\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Monorepo\n"), 0644)

	configJSON := `{
  "version": "1.0",
  "packages": [
    {
      "name": "proto",
      "path": "packages/proto",
      "language": "proto",
      "dependencies": []
    },
    {
      "name": "contracts",
      "path": "packages/contracts",
      "language": "typescript",
      "dependencies": ["proto"]
    },
    {
      "name": "billing",
      "path": "services/billing",
      "language": "go",
      "dependencies": ["contracts"]
    },
    {
      "name": "web-frontend",
      "path": "apps/web-frontend",
      "language": "typescript",
      "dependencies": []
    }
  ]
}`
	_ = os.WriteFile(filepath.Join(tmpDir, ".agents", "monoflow.config.json"), []byte(configJSON), 0644)

	run("add", ".")
	run("commit", "-m", "initial commit with 4 packages")

	return tmpDir
}

func TestResolveSubsetAndDependencies(t *testing.T) {
	repoDir := setupTestMonorepo(t)
	defer os.RemoveAll(repoDir)

	g, err := graph.LoadOrDiscover(repoDir)
	if err != nil {
		t.Fatalf("failed to load graph: %v", err)
	}

	// Request ONLY "billing"
	// Should include: "billing", plus upstream "contracts", plus upstream "proto"
	// Should omit: "web-frontend"
	included, omitted, sparsePaths, err := Resolve(g, []string{"billing"}, true)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(included) != 3 {
		t.Fatalf("expected 3 included packages (billing, contracts, proto), got: %v", included)
	}
	expectedIncluded := map[string]bool{"billing": true, "contracts": true, "proto": true}
	for _, inc := range included {
		if !expectedIncluded[inc] {
			t.Errorf("unexpected included package: %s", inc)
		}
	}

	if len(omitted) != 1 || omitted[0] != "web-frontend" {
		t.Fatalf("expected web-frontend omitted, got: %v", omitted)
	}

	// Verify sparsePaths contain .agents, services/billing, packages/contracts, packages/proto
	pathMap := make(map[string]bool)
	for _, p := range sparsePaths {
		pathMap[p] = true
	}
	if !pathMap["services/billing"] || !pathMap["packages/contracts"] || !pathMap["packages/proto"] || !pathMap[".agents"] {
		t.Errorf("unexpected sparsePaths: %v", sparsePaths)
	}
}

func TestResolveWithoutDependencies(t *testing.T) {
	repoDir := setupTestMonorepo(t)
	defer os.RemoveAll(repoDir)

	g, err := graph.LoadOrDiscover(repoDir)
	if err != nil {
		t.Fatalf("failed to load graph: %v", err)
	}

	// Request "billing" with includeDeps = false
	included, omitted, _, err := Resolve(g, []string{"billing"}, false)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(included) != 1 || included[0] != "billing" {
		t.Fatalf("expected only billing included, got: %v", included)
	}
	if len(omitted) != 3 {
		t.Fatalf("expected 3 omitted packages, got: %v", omitted)
	}
}

func TestApplyAndResetSubset(t *testing.T) {
	repoDir := setupTestMonorepo(t)
	defer os.RemoveAll(repoDir)

	// Apply subset for "contracts"
	res, err := Apply(repoDir, []string{"contracts"}, true)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if res.Status != "success" {
		t.Errorf("unexpected status: %s", res.Status)
	}
	if res.SubsetPackagesCount != 2 { // contracts + proto
		t.Errorf("expected 2 packages in subset, got: %d", res.SubsetPackagesCount)
	}

	// Verify apps/web-frontend and services/billing are no longer present on disk!
	if _, err := os.Stat(filepath.Join(repoDir, "apps", "web-frontend")); !os.IsNotExist(err) {
		t.Errorf("expected apps/web-frontend to be omitted from disk, but it still exists")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "services", "billing")); !os.IsNotExist(err) {
		t.Errorf("expected services/billing to be omitted from disk, but it still exists")
	}

	// Verify packages/contracts, packages/proto, README.md, go.mod exist!
	if _, err := os.Stat(filepath.Join(repoDir, "packages", "contracts", "index.ts")); err != nil {
		t.Errorf("expected packages/contracts/index.ts to exist on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "packages", "proto", "schema.proto")); err != nil {
		t.Errorf("expected packages/proto/schema.proto to exist on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "README.md")); err != nil {
		t.Errorf("expected root README.md to exist: %v", err)
	}

	// Check status
	st, err := Status(repoDir)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !st.IsSparse {
		t.Errorf("expected status IsSparse to be true")
	}

	// Reset subset
	err = Reset(repoDir)
	if err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	// Verify all packages are restored!
	if _, err := os.Stat(filepath.Join(repoDir, "apps", "web-frontend", "index.html")); err != nil {
		t.Errorf("expected apps/web-frontend restored after reset: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "services", "billing", "service.go")); err != nil {
		t.Errorf("expected services/billing restored after reset: %v", err)
	}
}

func TestCloneSubset(t *testing.T) {
	originRepo := setupTestMonorepo(t)
	defer os.RemoveAll(originRepo)

	cloneDest, err := os.MkdirTemp("", "monoflow_clone_dest_*")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(cloneDest) // ensure directory does not exist before clone
	defer os.RemoveAll(cloneDest)

	// Clone ONLY "billing" (which includes billing, contracts, proto; omits web-frontend)
	res, err := Clone(originRepo, cloneDest, []string{"billing"}, DefaultCloneOptions())
	if err != nil {
		t.Fatalf("Clone failed: %v", err)
	}

	if res.Status != "success" {
		t.Errorf("expected success status, got %s", res.Status)
	}
	if res.SubsetPackagesCount != 3 {
		t.Errorf("expected 3 packages, got %d", res.SubsetPackagesCount)
	}

	// Verify web-frontend is NOT on disk
	if _, err := os.Stat(filepath.Join(cloneDest, "apps", "web-frontend")); !os.IsNotExist(err) {
		t.Errorf("expected web-frontend to be omitted in cloned subset")
	}

	// Verify billing, contracts, proto ARE on disk
	if _, err := os.Stat(filepath.Join(cloneDest, "services", "billing", "service.go")); err != nil {
		t.Errorf("expected services/billing to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cloneDest, "packages", "contracts", "index.ts")); err != nil {
		t.Errorf("expected packages/contracts to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cloneDest, "packages", "proto", "schema.proto")); err != nil {
		t.Errorf("expected packages/proto to exist: %v", err)
	}
}

func TestAddPackageToSubset(t *testing.T) {
	repoDir := setupTestMonorepo(t)
	defer os.RemoveAll(repoDir)

	// Start with only proto
	_, err := Apply(repoDir, []string{"proto"}, false)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Verify only proto is present
	if _, err := os.Stat(filepath.Join(repoDir, "packages", "contracts")); !os.IsNotExist(err) {
		t.Errorf("expected contracts to be omitted initially")
	}

	// Add contracts to the active subset
	addRes, err := Add(repoDir, []string{"contracts"}, DepStrategySource)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if addRes.SubsetPackagesCount != 2 { // contracts + proto
		t.Errorf("expected 2 packages, got: %d", addRes.SubsetPackagesCount)
	}

	// Verify contracts is now materialized on disk!
	if _, err := os.Stat(filepath.Join(repoDir, "packages", "contracts", "index.ts")); err != nil {
		t.Errorf("expected contracts to exist after Add: %v", err)
	}
	// Verify billing is still omitted!
	if _, err := os.Stat(filepath.Join(repoDir, "services", "billing")); !os.IsNotExist(err) {
		t.Errorf("expected billing to remain omitted after Add")
	}
}

func TestRemovePackageDependencySafety(t *testing.T) {
	repoDir := setupTestMonorepo(t)
	defer os.RemoveAll(repoDir)

	// Set subset to billing and web-frontend (billing pulls contracts and proto)
	_, err := Apply(repoDir, []string{"billing", "web-frontend"}, true)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Attempt to remove "contracts" WITHOUT force
	// Billing depends on contracts! Should fail with safety error.
	_, err = Remove(repoDir, []string{"contracts"}, false)
	if err == nil {
		t.Fatalf("expected error trying to remove contracts while billing is active, but succeeded")
	}
	if !strings.Contains(err.Error(), "billing") || !strings.Contains(err.Error(), "depends on it") {
		t.Errorf("unexpected error message: %v", err)
	}

	// Remove "web-frontend" (nothing depends on web-frontend) -> should succeed!
	remRes, err := Remove(repoDir, []string{"web-frontend"}, false)
	if err != nil {
		t.Fatalf("Remove web-frontend failed: %v", err)
	}
	if remRes.SubsetPackagesCount != 3 { // billing, contracts, proto
		t.Errorf("expected 3 remaining packages, got %d", remRes.SubsetPackagesCount)
	}

	// Verify web-frontend is pruned from disk
	if _, err := os.Stat(filepath.Join(repoDir, "apps", "web-frontend")); !os.IsNotExist(err) {
		t.Errorf("expected web-frontend pruned from disk")
	}

	// Now remove "contracts" with --force -> should succeed
	forceRes, err := Remove(repoDir, []string{"contracts"}, true)
	if err != nil {
		t.Fatalf("Remove with force failed: %v", err)
	}
	_ = forceRes
}

func TestUpdatePackageFromRemote(t *testing.T) {
	originRepo := setupTestMonorepo(t)
	defer os.RemoveAll(originRepo)

	// Clone subset with billing and contracts
	cloneDest, err := os.MkdirTemp("", "monoflow_update_dest_*")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(cloneDest)
	defer os.RemoveAll(cloneDest)

	_, err = Clone(originRepo, cloneDest, []string{"billing"}, DefaultCloneOptions())
	if err != nil {
		t.Fatalf("Clone failed: %v", err)
	}

	// In originRepo, make an edit in contracts/index.ts and an edit in apps/web-frontend/index.html
	_ = os.WriteFile(filepath.Join(originRepo, "packages/contracts/index.ts"), []byte("export const v = 2; // updated\n"), 0644)
	_ = os.WriteFile(filepath.Join(originRepo, "apps/web-frontend/index.html"), []byte("<h1>Updated Web</h1>\n"), 0644)

	cmdAdd := exec.Command("git", "add", ".")
	cmdAdd.Dir = originRepo
	_ = cmdAdd.Run()

	cmdCommit := exec.Command("git", "commit", "-m", "remote updates to contracts and web")
	cmdCommit.Dir = originRepo
	_ = cmdCommit.Run()

	// In cloneDest, update ONLY "contracts" from remote origin main
	upRes, err := Update(cloneDest, []string{"contracts"}, "origin", "main")
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if upRes.Status != "updated" {
		t.Errorf("expected updated status, got: %s", upRes.Status)
	}
	if len(upRes.UpdatedPackages) != 1 || upRes.UpdatedPackages[0] != "contracts" {
		t.Errorf("expected contracts in updated packages, got: %v", upRes.UpdatedPackages)
	}

	// Verify contracts in cloneDest has the new content!
	cContent, _ := os.ReadFile(filepath.Join(cloneDest, "packages/contracts/index.ts"))
	if !strings.Contains(string(cContent), "export const v = 2") {
		t.Errorf("expected updated content in contracts, got: %s", string(cContent))
	}

	// Verify apps/web-frontend is STILL NOT on disk in cloneDest! (untouched)
	if _, err := os.Stat(filepath.Join(cloneDest, "apps", "web-frontend")); !os.IsNotExist(err) {
		t.Errorf("expected web-frontend to remain omitted on disk in cloneDest")
	}
}

func TestInspectDeps(t *testing.T) {
	repoDir := setupTestMonorepo(t)
	defer os.RemoveAll(repoDir)

	g, err := graph.LoadOrDiscover(repoDir)
	if err != nil {
		t.Fatalf("LoadOrDiscover failed: %v", err)
	}

	report, err := InspectDeps(g, "billing", repoDir)
	if err != nil {
		t.Fatalf("InspectDeps failed: %v", err)
	}

	if report.Package != "billing" {
		t.Errorf("unexpected package: %s", report.Package)
	}
	if len(report.DirectUpstream) != 1 || report.DirectUpstream[0] != "contracts" {
		t.Errorf("expected direct upstream contracts, got: %v", report.DirectUpstream)
	}
	if len(report.TransitiveUpstream) != 2 { // contracts, proto
		t.Errorf("expected 2 transitive upstream packages, got: %v", report.TransitiveUpstream)
	}
	if report.RecommendedStrategy == "" {
		t.Errorf("expected recommended strategy, got empty")
	}
}

