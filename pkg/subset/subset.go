package subset

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/graph"
)

// CloneOptions configures the subset clone operation.
type CloneOptions struct {
	IncludeDeps bool   // Automatically include upstream transitive dependencies (default: true)
	Branch      string // Git branch or commit to checkout (optional)
	PartialBlob bool   // Use Git partial clone --filter=blob:none (default: true)
	Cone        bool   // Use Git sparse-checkout in cone mode (default: true)
	Depth       int    // Optional shallow depth
}

// DefaultCloneOptions returns standard clone settings.
func DefaultCloneOptions() CloneOptions {
	return CloneOptions{
		IncludeDeps: true,
		PartialBlob: true,
		Cone:        true,
		Depth:       0,
	}
}

// SubsetResult records the outcome of a subset clone or checkout.
type SubsetResult struct {
	Status              string   `json:"status"`               // "success", "already_subset", "error"
	TargetDir           string   `json:"target_dir"`           // Path to the repository
	RequestedPackages   []string `json:"requested_packages"`   // Packages requested by the user/agent
	IncludedPackages    []string `json:"included_packages"`    // Target packages + upstream dependencies
	OmittedPackages     []string `json:"omitted_packages"`     // Monorepo packages omitted from disk
	SparsePaths         []string `json:"sparse_paths"`         // Directory paths included in sparse checkout
	TotalPackagesCount  int      `json:"total_packages_count"` // Total packages in monorepo
	SubsetPackagesCount int      `json:"subset_packages_count"`// Number of packages checked out
	DiskSavingsPct      float64  `json:"disk_savings_pct"`     // Estimated or calculated savings %
	DurationMs          float64  `json:"duration_ms"`
	Method              string   `json:"method"` // e.g. "git-partial-sparse-cone"
	Message             string   `json:"message"`
}

// SubsetStatus reports current sparse checkout status of a repository.
type SubsetStatus struct {
	IsSparse           bool     `json:"is_sparse"`
	RequestedPackages  []string `json:"requested_packages,omitempty"`
	IncludedPackages   []string `json:"included_packages"`
	OmittedPackages    []string `json:"omitted_packages"`
	SparsePaths        []string `json:"sparse_paths"`
	TotalPackagesCount int      `json:"total_packages_count"`
}

// SubsetMetadata is persisted in .agents/subset.json
type SubsetMetadata struct {
	RequestedPackages []string  `json:"requested_packages"`
	IncludedPackages  []string  `json:"included_packages"`
	OmittedPackages   []string  `json:"omitted_packages"`
	SparsePaths       []string  `json:"sparse_paths"`
	CreatedAt         time.Time `json:"created_at"`
	Method            string    `json:"method"`
}

// Resolve identifies which packages to check out, resolving upstream dependencies and computing sparse paths.
func Resolve(g *graph.DependencyGraph, targets []string, includeDeps bool) (included []string, omitted []string, sparsePaths []string, err error) {
	allPackages := g.GetAllPackages()
	if len(allPackages) == 0 {
		return nil, nil, nil, fmt.Errorf("no packages discovered in monorepo graph")
	}

	allMap := make(map[string]*graph.PackageNode)
	allNames := make([]string, 0, len(allPackages))
	for _, p := range allPackages {
		allMap[p.Name] = p
		allNames = append(allNames, p.Name)
	}

	// Normalize target list (expand comma-separated items and trimmed strings)
	cleanTargets := make([]string, 0)
	for _, t := range targets {
		parts := strings.Split(t, ",")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				cleanTargets = append(cleanTargets, trimmed)
			}
		}
	}

	if len(cleanTargets) == 0 {
		return nil, nil, nil, fmt.Errorf("no target packages specified")
	}

	// Match targets against graph packages
	matchedPackages := make(map[string]*graph.PackageNode)
	for _, target := range cleanTargets {
		p := findMatchingPackage(allPackages, target)
		if p == nil {
			return nil, nil, nil, fmt.Errorf("package '%s' not found in monorepo graph (available: %s)", target, strings.Join(allNames, ", "))
		}
		matchedPackages[p.Name] = p
	}

	// Resolve upstream transitive dependencies if requested
	includedMap := make(map[string]bool)
	for name := range matchedPackages {
		includedMap[name] = true
		if includeDeps {
			upstream := g.GetUpstream(name)
			for _, u := range upstream {
				includedMap[u] = true
				// Traverse recursively with cycle safety
				traverseTransitiveUpstream(g, u, includedMap)
			}
		}
	}

	// Compute sorted included and omitted lists
	included = make([]string, 0, len(includedMap))
	for name := range includedMap {
		included = append(included, name)
	}
	sort.Strings(included)

	omittedMap := make(map[string]bool)
	for _, p := range allPackages {
		if !includedMap[p.Name] {
			omittedMap[p.Name] = true
		}
	}
	omitted = make([]string, 0, len(omittedMap))
	for name := range omittedMap {
		omitted = append(omitted, name)
	}
	sort.Strings(omitted)

	// Compute sparse directory paths
	pathSet := make(map[string]bool)
	// Always include .agents directory for monorepo configuration, tasks, and telemetry
	pathSet[".agents"] = true

	for _, name := range included {
		p := allMap[name]
		if p == nil {
			continue
		}
		cleanPath := filepath.Clean(p.Path)
		cleanPath = strings.ReplaceAll(cleanPath, "\\", "/")
		cleanPath = strings.TrimPrefix(cleanPath, "/")

		// If package path is ".", root is included; cone mode includes all root files by default
		if cleanPath != "." && cleanPath != "" {
			pathSet[cleanPath] = true
		}
	}

	sparsePaths = make([]string, 0, len(pathSet))
	for p := range pathSet {
		sparsePaths = append(sparsePaths, p)
	}
	sort.Strings(sparsePaths)

	return included, omitted, sparsePaths, nil
}

func traverseTransitiveUpstream(g *graph.DependencyGraph, current string, visited map[string]bool) {
	for _, u := range g.GetUpstream(current) {
		if !visited[u] {
			visited[u] = true
			traverseTransitiveUpstream(g, u, visited)
		}
	}
}

func findMatchingPackage(packages []*graph.PackageNode, target string) *graph.PackageNode {
	cleanTarget := filepath.Clean(target)
	cleanTarget = strings.ReplaceAll(cleanTarget, "\\", "/")
	cleanTarget = strings.TrimPrefix(cleanTarget, "/")

	// 1. Exact Name match
	for _, p := range packages {
		if p.Name == target || p.Name == cleanTarget {
			return p
		}
	}

	// 2. Exact Path match
	for _, p := range packages {
		pkgPath := filepath.Clean(p.Path)
		pkgPath = strings.ReplaceAll(pkgPath, "\\", "/")
		pkgPath = strings.TrimPrefix(pkgPath, "/")
		if pkgPath == cleanTarget {
			return p
		}
	}

	// 3. Match without npm scope (e.g. "contracts" for "@acme/contracts")
	for _, p := range packages {
		if idx := strings.Index(p.Name, "/"); idx != -1 {
			unscoped := p.Name[idx+1:]
			if unscoped == target {
				return p
			}
		}
	}

	// 4. Base name match of path
	for _, p := range packages {
		if filepath.Base(p.Path) == target {
			return p
		}
	}

	return nil
}

// Clone clones a remote or local repository with ONLY a subset of packages and their dependencies.
func Clone(remoteURL, destDir string, targets []string, opts CloneOptions) (*SubsetResult, error) {
	start := time.Now()

	if destDir == "" {
		// Default to basename of repo URL
		base := filepath.Base(strings.TrimSuffix(remoteURL, ".git"))
		if base == "" || base == "." {
			base = "monorepo-subset"
		}
		destDir = base
	}

	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(absDest), 0755); err != nil {
		return nil, err
	}

	// Step 1: Run git clone with partial blob filter and --no-checkout
	args := []string{"clone"}
	if opts.PartialBlob {
		args = append(args, "--filter=blob:none")
	}
	args = append(args, "--no-checkout")
	if opts.Branch != "" {
		args = append(args, "--branch", opts.Branch)
	}
	if opts.Depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", opts.Depth))
	}
	args = append(args, remoteURL, absDest)

	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil && opts.PartialBlob {
		// Fallback without --filter=blob:none if remote server / transport does not support partial clone
		_ = os.RemoveAll(absDest)
		argsFallback := []string{"clone", "--no-checkout"}
		if opts.Branch != "" {
			argsFallback = append(argsFallback, "--branch", opts.Branch)
		}
		if opts.Depth > 0 {
			argsFallback = append(argsFallback, "--depth", fmt.Sprintf("%d", opts.Depth))
		}
		argsFallback = append(argsFallback, remoteURL, absDest)

		cmdFallback := exec.Command("git", argsFallback...)
		outFallback, errFallback := cmdFallback.CombinedOutput()
		if errFallback != nil {
			return nil, fmt.Errorf("git clone failed: %w (%s)", errFallback, strings.TrimSpace(string(outFallback)))
		}
	} else if err != nil {
		return nil, fmt.Errorf("git clone failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	// Step 2: Initialize sparse checkout cone mode
	cmdInit := exec.Command("git", "sparse-checkout", "init", "--cone")
	cmdInit.Dir = absDest
	if outInit, errInit := cmdInit.CombinedOutput(); errInit != nil {
		return nil, fmt.Errorf("git sparse-checkout init failed: %w (%s)", errInit, strings.TrimSpace(string(outInit)))
	}

	// Step 3: Discover monorepo graph from repo
	g, err := loadGraphFromGit(absDest)
	if err != nil {
		return nil, fmt.Errorf("failed to discover monorepo graph in cloned repo: %w", err)
	}

	// Step 4: Resolve requested packages and dependencies
	included, omitted, sparsePaths, err := Resolve(g, targets, opts.IncludeDeps)
	if err != nil {
		return nil, err
	}

	// Step 5: Configure sparse-checkout paths
	setArgs := append([]string{"sparse-checkout", "set"}, sparsePaths...)
	cmdSet := exec.Command("git", setArgs...)
	cmdSet.Dir = absDest
	if outSet, errSet := cmdSet.CombinedOutput(); errSet != nil {
		return nil, fmt.Errorf("git sparse-checkout set failed: %w (%s)", errSet, strings.TrimSpace(string(outSet)))
	}

	// Step 6: Checkout HEAD
	cmdCheckout := exec.Command("git", "checkout", "HEAD")
	cmdCheckout.Dir = absDest
	if outCheckout, errCheckout := cmdCheckout.CombinedOutput(); errCheckout != nil {
		return nil, fmt.Errorf("git checkout HEAD failed: %w (%s)", errCheckout, strings.TrimSpace(string(outCheckout)))
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	totalCount := len(g.GetAllPackages())
	savingsPct := 0.0
	if totalCount > 0 {
		savingsPct = float64(len(omitted)) / float64(totalCount) * 100.0
	}

	// Step 7: Persist subset metadata
	_ = saveSubsetMetadata(absDest, SubsetMetadata{
		RequestedPackages: targets,
		IncludedPackages:  included,
		OmittedPackages:   omitted,
		SparsePaths:       sparsePaths,
		CreatedAt:         time.Now().UTC(),
		Method:            "git-partial-sparse-cone",
	})

	return &SubsetResult{
		Status:              "success",
		TargetDir:           absDest,
		RequestedPackages:   targets,
		IncludedPackages:    included,
		OmittedPackages:     omitted,
		SparsePaths:         sparsePaths,
		TotalPackagesCount:  totalCount,
		SubsetPackagesCount: len(included),
		DiskSavingsPct:      savingsPct,
		DurationMs:          durMs,
		Method:              "git-partial-sparse-cone",
		Message:             fmt.Sprintf("Cloned subset of %d/%d packages in %.2fms (omitted %d packages, ~%.1f%% package reduction)", len(included), totalCount, durMs, len(omitted), savingsPct),
	}, nil
}

// Apply configures an existing repository or worktree to only materialize the requested subset of packages.
func Apply(repoDir string, targets []string, includeDeps bool) (*SubsetResult, error) {
	start := time.Now()

	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}

	g, err := graph.LoadOrDiscover(absRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to load monorepo graph: %w", err)
	}

	included, omitted, sparsePaths, err := Resolve(g, targets, includeDeps)
	if err != nil {
		return nil, err
	}

	// Initialize sparse-checkout
	cmdInit := exec.Command("git", "sparse-checkout", "init", "--cone")
	cmdInit.Dir = absRepo
	if outInit, errInit := cmdInit.CombinedOutput(); errInit != nil {
		return nil, fmt.Errorf("git sparse-checkout init failed: %w (%s)", errInit, strings.TrimSpace(string(outInit)))
	}

	// Set sparse paths
	setArgs := append([]string{"sparse-checkout", "set"}, sparsePaths...)
	cmdSet := exec.Command("git", setArgs...)
	cmdSet.Dir = absRepo
	if outSet, errSet := cmdSet.CombinedOutput(); errSet != nil {
		return nil, fmt.Errorf("git sparse-checkout set failed: %w (%s)", errSet, strings.TrimSpace(string(outSet)))
	}

	// Refresh checkout to prune omitted packages immediately
	cmdCheckout := exec.Command("git", "checkout", "HEAD")
	cmdCheckout.Dir = absRepo
	_, _ = cmdCheckout.CombinedOutput()

	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	totalCount := len(g.GetAllPackages())
	savingsPct := 0.0
	if totalCount > 0 {
		savingsPct = float64(len(omitted)) / float64(totalCount) * 100.0
	}

	_ = saveSubsetMetadata(absRepo, SubsetMetadata{
		RequestedPackages: targets,
		IncludedPackages:  included,
		OmittedPackages:   omitted,
		SparsePaths:       sparsePaths,
		CreatedAt:         time.Now().UTC(),
		Method:            "git-sparse-cone",
	})

	return &SubsetResult{
		Status:              "success",
		TargetDir:           absRepo,
		RequestedPackages:   targets,
		IncludedPackages:    included,
		OmittedPackages:     omitted,
		SparsePaths:         sparsePaths,
		TotalPackagesCount:  totalCount,
		SubsetPackagesCount: len(included),
		DiskSavingsPct:      savingsPct,
		DurationMs:          durMs,
		Method:              "git-sparse-cone",
		Message:             fmt.Sprintf("Switched workspace to subset of %d/%d packages in %.2fms (omitted %d packages)", len(included), totalCount, durMs, len(omitted)),
	}, nil
}

// Status returns current sparse checkout and package subset details for a repository.
func Status(repoDir string) (*SubsetStatus, error) {
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}

	// Check if sparse-checkout is enabled in git
	cmd := exec.Command("git", "sparse-checkout", "list")
	cmd.Dir = absRepo
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Not sparse or git error
		return &SubsetStatus{
			IsSparse:           false,
			IncludedPackages:   []string{},
			OmittedPackages:    []string{},
			SparsePaths:        []string{},
			TotalPackagesCount: 0,
		}, nil
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var activePaths []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			activePaths = append(activePaths, trimmed)
		}
	}

	meta, _ := loadSubsetMetadata(absRepo)
	g, _ := graph.LoadOrDiscover(absRepo)

	totalCount := 0
	if g != nil {
		totalCount = len(g.GetAllPackages())
	}

	if meta != nil {
		return &SubsetStatus{
			IsSparse:           len(activePaths) > 0,
			RequestedPackages:  meta.RequestedPackages,
			IncludedPackages:   meta.IncludedPackages,
			OmittedPackages:    meta.OmittedPackages,
			SparsePaths:        activePaths,
			TotalPackagesCount: totalCount,
		}, nil
	}

	return &SubsetStatus{
		IsSparse:           len(activePaths) > 0,
		SparsePaths:        activePaths,
		TotalPackagesCount: totalCount,
	}, nil
}

// Reset disables sparse-checkout, bringing back the full monorepo checkout.
func Reset(repoDir string) error {
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return err
	}

	cmd := exec.Command("git", "sparse-checkout", "disable")
	cmd.Dir = absRepo
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git sparse-checkout disable failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	cmdCheckout := exec.Command("git", "checkout", "HEAD")
	cmdCheckout.Dir = absRepo
	_, _ = cmdCheckout.CombinedOutput()

	_ = os.Remove(filepath.Join(absRepo, ".agents", "subset.json"))
	return nil
}

func loadGraphFromGit(repoDir string) (*graph.DependencyGraph, error) {
	// 1. Try reading .agents/monoflow.config.json from HEAD without checking it out
	cmd := exec.Command("git", "show", "HEAD:.agents/monoflow.config.json")
	cmd.Dir = repoDir
	if out, err := cmd.Output(); err == nil && len(out) > 0 {
		var cfg graph.Config
		if err := json.Unmarshal(out, &cfg); err == nil && len(cfg.Packages) > 0 {
			g := graph.New(repoDir)
			for _, p := range cfg.Packages {
				pCopy := p
				g.AddPackage(&pCopy)
			}
			g.RebuildEdges()
			return g, nil
		}
	}

	// 2. Checkout root metadata files to allow discovery
	cmdSet := exec.Command("git", "sparse-checkout", "set", ".agents")
	cmdSet.Dir = repoDir
	_ = cmdSet.Run()

	cmdCheckout := exec.Command("git", "checkout", "HEAD")
	cmdCheckout.Dir = repoDir
	_ = cmdCheckout.Run()

	return graph.LoadOrDiscover(repoDir)
}

func saveSubsetMetadata(repoDir string, meta SubsetMetadata) error {
	agentsDir := filepath.Join(repoDir, ".agents")
	_ = os.MkdirAll(agentsDir, 0755)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(agentsDir, "subset.json"), data, 0644)
}

func loadSubsetMetadata(repoDir string) (*SubsetMetadata, error) {
	metaFile := filepath.Join(repoDir, ".agents", "subset.json")
	data, err := os.ReadFile(metaFile)
	if err != nil {
		return nil, err
	}
	var meta SubsetMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}
