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

	"github.com/antigravity/monoflow/pkg/checkpoint"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/runtimestate"
)

// DepStrategy defines how upstream monorepo dependencies are handled.
type DepStrategy string

const (
	// DepStrategySource materializes all upstream dependencies as full source code (default).
	DepStrategySource DepStrategy = "source"
	// DepStrategyContracts materializes only contracts / proto / schema declaration packages.
	DepStrategyContracts DepStrategy = "contracts"
	// DepStrategyNone strictly materializes the target package with zero upstream monorepo dependencies.
	DepStrategyNone DepStrategy = "none"
)

// CloneOptions configures the subset clone operation.
type CloneOptions struct {
	Strategy    DepStrategy // Dependency handling strategy (source, contracts, none)
	IncludeDeps bool        // Convenience boolean for backward compatibility (default: true)
	Branch      string      // Git branch or commit to checkout (optional)
	PartialBlob bool        // Use Git partial clone --filter=blob:none (default: true)
	Cone        bool        // Use Git sparse-checkout in cone mode (default: true)
	Depth       int         // Optional shallow depth
}

// DefaultCloneOptions returns standard clone settings.
func DefaultCloneOptions() CloneOptions {
	return CloneOptions{
		Strategy:    DepStrategySource,
		IncludeDeps: true,
		PartialBlob: true,
		Cone:        true,
		Depth:       0,
	}
}

// SubsetResult records the outcome of a subset clone or checkout.
type SubsetResult struct {
	Status              string   `json:"status"`                // "success", "already_subset", "error"
	TargetDir           string   `json:"target_dir"`            // Path to the repository
	RequestedPackages   []string `json:"requested_packages"`    // Packages requested by the user/agent
	IncludedPackages    []string `json:"included_packages"`     // Target packages + upstream dependencies
	OmittedPackages     []string `json:"omitted_packages"`      // Monorepo packages omitted from disk
	SparsePaths         []string `json:"sparse_paths"`          // Directory paths included in sparse checkout
	TotalPackagesCount  int      `json:"total_packages_count"`  // Total packages in monorepo
	SubsetPackagesCount int      `json:"subset_packages_count"` // Number of packages checked out
	DiskSavingsPct      float64  `json:"disk_savings_pct"`      // Estimated or calculated savings %
	DurationMs          float64  `json:"duration_ms"`
	Strategy            string   `json:"strategy,omitempty"`
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
	Strategy           string   `json:"strategy,omitempty"`
}

// SubsetMetadata is persisted in .agents/subset.json
type SubsetMetadata struct {
	RequestedPackages []string  `json:"requested_packages"`
	IncludedPackages  []string  `json:"included_packages"`
	OmittedPackages   []string  `json:"omitted_packages"`
	SparsePaths       []string  `json:"sparse_paths"`
	Strategy          string    `json:"strategy,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	Method            string    `json:"method"`
}

// UpdateResult records the outcome of selectively updating packages from a remote.
type UpdateResult struct {
	Status          string   `json:"status"`
	Remote          string   `json:"remote"`
	Branch          string   `json:"branch"`
	UpdatedPackages []string `json:"updated_packages"`
	UpdatedFiles    []string `json:"updated_files"`
	DurationMs      float64  `json:"duration_ms"`
	Message         string   `json:"message"`
}

// DepsReport provides detailed dependency analysis and strategy recommendations.
type DepsReport struct {
	Package              string      `json:"package"`
	Version              string      `json:"version,omitempty"`
	Path                 string      `json:"path"`
	Language             string      `json:"language"`
	DirectUpstream       []string    `json:"direct_upstream"`
	TransitiveUpstream   []string    `json:"transitive_upstream"`
	DirectDownstream     []string    `json:"direct_downstream"`
	TransitiveDownstream []string    `json:"transitive_downstream"`
	DependenciesStatus   []DepStatus `json:"dependencies_status"`
	RecommendedStrategy  string      `json:"recommended_strategy"`
}

// DepStatus represents the materialization state of an individual dependency.
type DepStatus struct {
	Name         string `json:"name"`
	Version      string `json:"version,omitempty"`
	Path         string `json:"path"`
	IsActive     bool   `json:"is_active"`
	Materialized bool   `json:"materialized"` // present on disk
	Relation     string `json:"relation"`     // "direct" or "transitive"
}

// SubsetCheckResult reports dependency health, completeness, and version compatibility in a subset.
type SubsetCheckResult struct {
	Status              string                `json:"status"` // "healthy", "warning", "error"
	IsSparse            bool                  `json:"is_sparse"`
	Strategy            string                `json:"strategy,omitempty"`
	ActivePackages      []string              `json:"active_packages"`
	OmittedPackages     []string              `json:"omitted_packages"`
	MissingDependencies []MissingDepViolation `json:"missing_dependencies,omitempty"`
	VersionMismatches   []VersionMismatch     `json:"version_mismatches,omitempty"`
	DurationMs          float64               `json:"duration_ms"`
	Verdict             string                `json:"verdict"`
}

// MissingDepViolation details an upstream dependency required by an active package that is omitted or missing.
type MissingDepViolation struct {
	ConsumerPackage   string `json:"consumer_package"`
	MissingDependency string `json:"missing_dependency"`
	ExpectedPath      string `json:"expected_path"`
	RemediationHint   string `json:"remediation_hint"`
}

// VersionMismatch details an incompatible version between active packages and dependencies.
type VersionMismatch struct {
	ConsumerPackage string `json:"consumer_package"`
	Dependency      string `json:"dependency"`
	RequiredVersion string `json:"required_version"`
	FoundVersion    string `json:"found_version"`
	Message         string `json:"message"`
}

// Resolve identifies which packages to check out with boolean includeDeps (defaults to source strategy).
func Resolve(g *graph.DependencyGraph, targets []string, includeDeps bool) (included []string, omitted []string, sparsePaths []string, err error) {
	strategy := DepStrategySource
	if !includeDeps {
		strategy = DepStrategyNone
	}
	return ResolveWithOptions(g, targets, strategy)
}

// ResolveWithOptions identifies packages to check out using a configurable dependency strategy.
func ResolveWithOptions(g *graph.DependencyGraph, targets []string, strategy DepStrategy) (included []string, omitted []string, sparsePaths []string, err error) {
	if strategy == "" {
		strategy = DepStrategySource
	}

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

	// Resolve upstream transitive dependencies based on strategy
	includedMap := make(map[string]bool)
	for name := range matchedPackages {
		includedMap[name] = true

		if strategy == DepStrategySource {
			upstream := g.GetUpstream(name)
			for _, u := range upstream {
				includedMap[u] = true
				traverseTransitiveUpstream(g, u, includedMap, strategy, allMap)
			}
		} else if strategy == DepStrategyContracts {
			upstream := g.GetUpstream(name)
			for _, u := range upstream {
				if isContractPackage(allMap[u]) {
					includedMap[u] = true
				}
				traverseTransitiveUpstream(g, u, includedMap, strategy, allMap)
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

func isContractPackage(p *graph.PackageNode) bool {
	if p == nil {
		return false
	}
	if p.Language == graph.LangProto {
		return true
	}
	lower := strings.ToLower(p.Name + " " + p.Path)
	return strings.Contains(lower, "contract") || strings.Contains(lower, "proto") || strings.Contains(lower, "schema")
}

func traverseTransitiveUpstream(g *graph.DependencyGraph, current string, visited map[string]bool, strategy DepStrategy, allMap map[string]*graph.PackageNode) {
	for _, u := range g.GetUpstream(current) {
		if !visited[u] {
			if strategy == DepStrategyContracts && !isContractPackage(allMap[u]) {
				continue
			}
			visited[u] = true
			traverseTransitiveUpstream(g, u, visited, strategy, allMap)
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

	strategy := opts.Strategy
	if strategy == "" {
		if opts.IncludeDeps {
			strategy = DepStrategySource
		} else {
			strategy = DepStrategyNone
		}
	}

	if destDir == "" {
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

	// Step 4: Resolve requested packages and dependencies using chosen strategy
	included, omitted, sparsePaths, err := ResolveWithOptions(g, targets, strategy)
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
		Strategy:          string(strategy),
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
		Strategy:            string(strategy),
		Method:              "git-partial-sparse-cone",
		Message:             fmt.Sprintf("Cloned subset of %d/%d packages in %.2fms (strategy: %s, omitted %d packages, ~%.1f%% package reduction)", len(included), totalCount, durMs, strategy, len(omitted), savingsPct),
	}, nil
}

// Apply configures an existing repository to only materialize the requested subset of packages.
func Apply(repoDir string, targets []string, includeDeps bool) (*SubsetResult, error) {
	strategy := DepStrategySource
	if !includeDeps {
		strategy = DepStrategyNone
	}
	return ApplyWithOptions(repoDir, targets, strategy)
}

// ApplyWithOptions configures an existing repository with an explicit dependency strategy.
func ApplyWithOptions(repoDir string, targets []string, strategy DepStrategy) (*SubsetResult, error) {
	start := time.Now()

	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}

	g, err := graph.LoadOrDiscover(absRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to load monorepo graph: %w", err)
	}

	included, omitted, sparsePaths, err := ResolveWithOptions(g, targets, strategy)
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

	_ = SyncGoWork(absRepo)

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
		Strategy:          string(strategy),
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
		Strategy:            string(strategy),
		Method:              "git-sparse-cone",
		Message:             fmt.Sprintf("Switched workspace to subset of %d/%d packages in %.2fms (omitted %d packages)", len(included), totalCount, durMs, len(omitted)),
	}, nil
}

// Add adds one or more packages to the active package subset without wiping existing selections.
func Add(repoDir string, targets []string, strategy DepStrategy) (*SubsetResult, error) {
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}

	meta, _ := loadSubsetMetadata(absRepo)
	existing := make(map[string]bool)
	var combined []string

	if meta != nil && len(meta.RequestedPackages) > 0 {
		for _, req := range meta.RequestedPackages {
			if !existing[req] {
				existing[req] = true
				combined = append(combined, req)
			}
		}
	}

	for _, t := range targets {
		parts := strings.Split(t, ",")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" && !existing[trimmed] {
				existing[trimmed] = true
				combined = append(combined, trimmed)
			}
		}
	}

	if strategy == "" && meta != nil && meta.Strategy != "" {
		strategy = DepStrategy(meta.Strategy)
	}

	return ApplyWithOptions(absRepo, combined, strategy)
}

// Remove removes one or more packages from the active subset with strict dependency safety.
func Remove(repoDir string, targets []string, force bool) (*SubsetResult, error) {
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}

	g, err := graph.LoadOrDiscover(absRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to load monorepo graph: %w", err)
	}

	meta, err := loadSubsetMetadata(absRepo)
	if err != nil || meta == nil || len(meta.RequestedPackages) == 0 {
		return nil, fmt.Errorf("repository is not currently in a package subset. Run 'monoflow subset set --packages=...' first")
	}

	// Identify packages to remove
	toRemoveMap := make(map[string]bool)
	for _, t := range targets {
		parts := strings.Split(t, ",")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed == "" {
				continue
			}
			p := findMatchingPackage(g.GetAllPackages(), trimmed)
			if p != nil {
				toRemoveMap[p.Name] = true
			} else {
				toRemoveMap[trimmed] = true
			}
		}
	}

	// Calculate remaining requested targets
	var remainingRequested []string
	for _, req := range meta.RequestedPackages {
		p := findMatchingPackage(g.GetAllPackages(), req)
		reqName := req
		if p != nil {
			reqName = p.Name
		}
		if !toRemoveMap[reqName] && !toRemoveMap[req] {
			remainingRequested = append(remainingRequested, req)
		}
	}

	if len(remainingRequested) == 0 {
		return nil, fmt.Errorf("cannot remove all packages from subset. To restore the full monorepo, run 'monoflow subset reset'")
	}

	// Dependency safety check: verify that no remaining package requires any package being removed
	if !force {
		allMap := make(map[string]*graph.PackageNode)
		for _, p := range g.GetAllPackages() {
			allMap[p.Name] = p
		}

		for remName := range toRemoveMap {
			for _, remPkgName := range remainingRequested {
				p := findMatchingPackage(g.GetAllPackages(), remPkgName)
				if p == nil {
					continue
				}
				upstreamMap := make(map[string]bool)
				upstream := g.GetUpstream(p.Name)
				for _, u := range upstream {
					upstreamMap[u] = true
					traverseTransitiveUpstream(g, u, upstreamMap, DepStrategySource, allMap)
				}

				if upstreamMap[remName] {
					return nil, fmt.Errorf("cannot remove package '%s': active package '%s' depends on it. Use --force to remove anyway, or remove '%s' first", remName, p.Name, p.Name)
				}
			}
		}
	}

	strategy := DepStrategySource
	if meta.Strategy != "" {
		strategy = DepStrategy(meta.Strategy)
	}

	return ApplyWithOptions(absRepo, remainingRequested, strategy)
}

// Update selectively pulls and updates specific packages from a remote branch without touching others.
func Update(repoDir string, targets []string, remote, branch string) (*UpdateResult, error) {
	start := time.Now()
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}

	if remote == "" {
		remote = "origin"
	}
	if branch == "" {
		branch = "main"
	}

	g, err := graph.LoadOrDiscover(absRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to load monorepo graph: %w", err)
	}

	// Determine which packages to update
	var pkgsToUpdate []*graph.PackageNode
	if len(targets) == 0 {
		// Update all active subset packages (or all packages if not sparse)
		meta, _ := loadSubsetMetadata(absRepo)
		if meta != nil && len(meta.IncludedPackages) > 0 {
			for _, name := range meta.IncludedPackages {
				if p, ok := g.GetPackage(name); ok {
					pkgsToUpdate = append(pkgsToUpdate, p)
				}
			}
		} else {
			pkgsToUpdate = g.GetAllPackages()
		}
	} else {
		for _, t := range targets {
			parts := strings.Split(t, ",")
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed == "" {
					continue
				}
				p := findMatchingPackage(g.GetAllPackages(), trimmed)
				if p == nil {
					return nil, fmt.Errorf("package '%s' not found in monorepo graph", trimmed)
				}
				pkgsToUpdate = append(pkgsToUpdate, p)
			}
		}
	}

	if len(pkgsToUpdate) == 0 {
		return &UpdateResult{
			Status:          "up_to_date",
			Remote:          remote,
			Branch:          branch,
			UpdatedPackages: []string{},
			UpdatedFiles:    []string{},
			DurationMs:      float64(time.Since(start).Microseconds()) / 1000.0,
			Message:         "No packages selected for update.",
		}, nil
	}

	// 1. Fetch remote branch commit/tree metadata with partial blob filter
	cmdFetch := exec.Command("git", "fetch", remote, branch, "--filter=blob:none")
	cmdFetch.Dir = absRepo
	if _, errFetch := cmdFetch.CombinedOutput(); errFetch != nil {
		// Fallback without filter if remote transport doesn't support blob filtering (e.g. file:// protocol)
		cmdFetchFallback := exec.Command("git", "fetch", remote, branch)
		cmdFetchFallback.Dir = absRepo
		if outFallback, errFallback := cmdFetchFallback.CombinedOutput(); errFallback != nil {
			return nil, fmt.Errorf("git fetch %s %s failed: %w (%s)", remote, branch, errFallback, strings.TrimSpace(string(outFallback)))
		}
	}

	remoteRef := "FETCH_HEAD"

	// 2. Identify diffs specifically for target package paths
	var updatedFiles []string
	var updatedPkgNames []string

	for _, p := range pkgsToUpdate {
		if p.Path == "" || p.Path == "." {
			continue
		}
		cmdDiff := exec.Command("git", "diff", "--name-only", "HEAD.."+remoteRef, "--", p.Path)
		cmdDiff.Dir = absRepo
		outDiff, errDiff := cmdDiff.Output()
		if errDiff != nil {
			continue
		}

		lines := strings.Split(strings.TrimSpace(string(outDiff)), "\n")
		var changedInPkg []string
		for _, l := range lines {
			trimmed := strings.TrimSpace(l)
			if trimmed != "" {
				changedInPkg = append(changedInPkg, trimmed)
			}
		}

		if len(changedInPkg) > 0 {
			// Check if locally dirty in this package
			cmdStatus := exec.Command("git", "status", "--porcelain", "--", p.Path)
			cmdStatus.Dir = absRepo
			outStatus, _ := cmdStatus.Output()
			if len(strings.TrimSpace(string(outStatus))) > 0 {
				// Save shadow checkpoint before applying update
				_, _ = checkpoint.NewManager(absRepo).Save(fmt.Sprintf("pre-update-%s", p.Name))
			}

			// Checkout remote version of these files
			cmdCheckout := exec.Command("git", "checkout", remoteRef, "--", p.Path)
			cmdCheckout.Dir = absRepo
			if outCo, errCo := cmdCheckout.CombinedOutput(); errCo != nil {
				return nil, fmt.Errorf("failed to update package '%s' from %s: %w (%s)", p.Name, remoteRef, errCo, strings.TrimSpace(string(outCo)))
			}

			updatedFiles = append(updatedFiles, changedInPkg...)
			updatedPkgNames = append(updatedPkgNames, p.Name)
		}
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0

	if len(updatedPkgNames) == 0 {
		return &UpdateResult{
			Status:          "up_to_date",
			Remote:          remote,
			Branch:          branch,
			UpdatedPackages: []string{},
			UpdatedFiles:    []string{},
			DurationMs:      durMs,
			Message:         fmt.Sprintf("All %d selected package(s) are already up to date with %s in %.2fms.", len(pkgsToUpdate), remoteRef, durMs),
		}, nil
	}

	return &UpdateResult{
		Status:          "updated",
		Remote:          remote,
		Branch:          branch,
		UpdatedPackages: updatedPkgNames,
		UpdatedFiles:    updatedFiles,
		DurationMs:      durMs,
		Message:         fmt.Sprintf("Selectively updated %d package(s) (%d files) from %s in %.2fms (other packages untouched).", len(updatedPkgNames), len(updatedFiles), remoteRef, durMs),
	}, nil
}

// InspectDeps provides a detailed dependency report for a package.
func InspectDeps(g *graph.DependencyGraph, target string, repoDir string) (*DepsReport, error) {
	allPackages := g.GetAllPackages()
	p := findMatchingPackage(allPackages, target)
	if p == nil {
		return nil, fmt.Errorf("package '%s' not found in monorepo graph", target)
	}

	allMap := make(map[string]*graph.PackageNode)
	for _, pkg := range allPackages {
		allMap[pkg.Name] = pkg
	}

	directUpstream := g.GetUpstream(p.Name)
	transitiveUpstreamMap := make(map[string]bool)
	for _, u := range directUpstream {
		transitiveUpstreamMap[u] = true
		traverseTransitiveUpstream(g, u, transitiveUpstreamMap, DepStrategySource, allMap)
	}
	delete(transitiveUpstreamMap, p.Name)

	var transitiveUpstream []string
	for u := range transitiveUpstreamMap {
		transitiveUpstream = append(transitiveUpstream, u)
	}
	sort.Strings(transitiveUpstream)

	directDownstream := g.GetDownstream(p.Name)
	transitiveDownstream := g.GetTransitiveDownstream(p.Name)

	meta, _ := loadSubsetMetadata(repoDir)
	activeMap := make(map[string]bool)
	if meta != nil {
		for _, inc := range meta.IncludedPackages {
			activeMap[inc] = true
		}
	} else {
		// All packages active if not sparse
		for _, pkg := range allPackages {
			activeMap[pkg.Name] = true
		}
	}

	var depStatusList []DepStatus
	for _, u := range transitiveUpstream {
		relation := "transitive"
		for _, d := range directUpstream {
			if d == u {
				relation = "direct"
				break
			}
		}

		uPkg := allMap[u]
		pkgPath := ""
		materialized := false
		if uPkg != nil {
			pkgPath = uPkg.Path
			if _, err := os.Stat(filepath.Join(repoDir, uPkg.Path)); err == nil {
				materialized = true
			}
		}

		uVer := "1.0.0"
		if uPkg != nil && uPkg.Version != "" {
			uVer = uPkg.Version
		}

		depStatusList = append(depStatusList, DepStatus{
			Name:         u,
			Version:      uVer,
			Path:         pkgPath,
			IsActive:     activeMap[u],
			Materialized: materialized,
			Relation:     relation,
		})
	}

	// Strategy recommendation
	recStrategy := "source (standard full-source dependency)"
	hasProtoOrContracts := false
	for _, u := range transitiveUpstream {
		if isContractPackage(allMap[u]) {
			hasProtoOrContracts = true
			break
		}
	}
	if len(transitiveUpstream) == 0 {
		recStrategy = "none (zero internal dependencies, isolated hermetic package)"
	} else if hasProtoOrContracts && len(transitiveUpstream) == 1 {
		recStrategy = "contracts (lightweight interface only; saves disk and indexing)"
	}

	pVer := "1.0.0"
	if p.Version != "" {
		pVer = p.Version
	}

	return &DepsReport{
		Package:              p.Name,
		Version:              pVer,
		Path:                 p.Path,
		Language:             string(p.Language),
		DirectUpstream:       directUpstream,
		TransitiveUpstream:   transitiveUpstream,
		DirectDownstream:     directDownstream,
		TransitiveDownstream: transitiveDownstream,
		DependenciesStatus:   depStatusList,
		RecommendedStrategy:  recStrategy,
	}, nil
}

// Status returns current sparse checkout and package subset details for a repository.
func Status(repoDir string) (*SubsetStatus, error) {
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}

	g, _ := graph.LoadOrDiscover(absRepo)
	totalCount := 0
	var allNames []string
	if g != nil {
		pkgs := g.GetAllPackages()
		totalCount = len(pkgs)
		for _, p := range pkgs {
			allNames = append(allNames, p.Name)
		}
	}

	cmd := exec.Command("git", "sparse-checkout", "list")
	cmd.Dir = absRepo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &SubsetStatus{
			IsSparse:           false,
			IncludedPackages:   allNames,
			OmittedPackages:    []string{},
			SparsePaths:        []string{},
			TotalPackagesCount: totalCount,
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

	if meta != nil {
		return &SubsetStatus{
			IsSparse:           len(activePaths) > 0,
			RequestedPackages:  meta.RequestedPackages,
			IncludedPackages:   meta.IncludedPackages,
			OmittedPackages:    meta.OmittedPackages,
			SparsePaths:        activePaths,
			TotalPackagesCount: totalCount,
			Strategy:           meta.Strategy,
		}, nil
	}

	isSparse := len(activePaths) > 0
	var inc []string
	var omit []string
	if isSparse {
		if g != nil {
			for _, p := range g.GetAllPackages() {
				matched := false
				for _, sp := range activePaths {
					if p.Path == sp || strings.HasPrefix(p.Path, sp+"/") {
						matched = true
						break
					}
				}
				if matched {
					inc = append(inc, p.Name)
				} else {
					omit = append(omit, p.Name)
				}
			}
		}
	} else {
		inc = allNames
	}

	return &SubsetStatus{
		IsSparse:           isSparse,
		IncludedPackages:   inc,
		OmittedPackages:    omit,
		SparsePaths:        activePaths,
		TotalPackagesCount: totalCount,
	}, nil
}

// CheckSubsetHealth validates dependency completeness, missing packages, and version compatibility in a subset.
func CheckSubsetHealth(repoDir string) (*SubsetCheckResult, error) {
	start := time.Now()
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}

	st, err := Status(absRepo)
	if err != nil {
		return nil, err
	}

	g, err := graph.LoadOrDiscover(absRepo)
	if err != nil {
		return nil, fmt.Errorf("failed to load dependency graph: %w", err)
	}

	allMap := make(map[string]*graph.PackageNode)
	for _, p := range g.GetAllPackages() {
		allMap[p.Name] = p
	}

	var missingDeps []MissingDepViolation
	var versionMismatches []VersionMismatch

	activeSet := make(map[string]bool)
	for _, p := range st.IncludedPackages {
		activeSet[p] = true
	}

	for _, activeName := range st.IncludedPackages {
		p := allMap[activeName]
		if p == nil {
			continue
		}

		for _, up := range g.GetUpstream(activeName) {
			upPkg := allMap[up]
			if upPkg == nil {
				continue
			}

			// Check if materialized on disk
			pkgDir := filepath.Join(absRepo, upPkg.Path)
			if upPkg.Path != "." && upPkg.Path != "" {
				if _, err := os.Stat(pkgDir); os.IsNotExist(err) {
					if !activeSet[up] {
						hint := fmt.Sprintf("Run 'monoflow subset add %s' or use '--deps=source' to include upstream dependency.", up)
						if isContractPackage(upPkg) {
							hint = fmt.Sprintf("Contract package '%s' is missing. Run 'monoflow subset add %s --deps=contracts'.", up, up)
						}
						missingDeps = append(missingDeps, MissingDepViolation{
							ConsumerPackage:   activeName,
							MissingDependency: up,
							ExpectedPath:      upPkg.Path,
							RemediationHint:   hint,
						})
					}
				}
			}
		}
	}

	status := "healthy"
	verdict := "All active packages in subset have their upstream dependencies and versions satisfied."
	if len(missingDeps) > 0 {
		status = "error"
		verdict = fmt.Sprintf("Detected %d missing upstream dependencies in current subset projection.", len(missingDeps))
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	return &SubsetCheckResult{
		Status:              status,
		IsSparse:            st.IsSparse,
		Strategy:            st.Strategy,
		ActivePackages:      st.IncludedPackages,
		OmittedPackages:     st.OmittedPackages,
		MissingDependencies: missingDeps,
		VersionMismatches:   versionMismatches,
		DurationMs:          durMs,
		Verdict:             verdict,
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

	restoreGoWork(absRepo)
	cmdCheckout := exec.Command("git", "checkout", "HEAD")
	cmdCheckout.Dir = absRepo
	_, _ = cmdCheckout.CombinedOutput()

	_ = os.Remove(filepath.Join(absRepo, ".agents", "subset.json"))
	return nil
}

// SyncGoWork rewrites a sparse checkout's go.work so it only "use"s modules that are
// present on disk; otherwise every go command fails on the omitted modules. The rewrite
// is derived from the committed go.work each time and hidden from git (skip-worktree)
// so it never leaks into commits.
func SyncGoWork(repoDir string) error {
	show := exec.Command("git", "show", "HEAD:go.work")
	show.Dir = repoDir
	orig, err := show.Output()
	if err != nil {
		return nil // no committed go.work
	}
	var out []string
	inUse := false
	pruned := false
	keep := func(entry string) bool {
		_, err := os.Stat(filepath.Join(repoDir, strings.Trim(entry, `"`), "go.mod"))
		return err == nil
	}
	for _, line := range strings.Split(string(orig), "\n") {
		t := strings.TrimSpace(line)
		if i := strings.Index(t, "//"); i >= 0 {
			t = strings.TrimSpace(t[:i])
		}
		switch {
		case t == "use (":
			inUse = true
		case inUse && t == ")":
			inUse = false
		case inUse && t != "" && !keep(t):
			pruned = true
			continue
		case strings.HasPrefix(t, "use ") && !strings.HasSuffix(t, "(") && !keep(strings.TrimSpace(strings.TrimPrefix(t, "use "))):
			pruned = true
			continue
		}
		out = append(out, line)
	}
	if !pruned {
		restoreGoWork(repoDir)
		return nil
	}
	projected := []byte(strings.Join(out, "\n"))
	if err := os.WriteFile(filepath.Join(repoDir, "go.work"), projected, 0644); err != nil {
		return err
	}
	// skip-worktree does not survive later sparse-checkout/index operations, so commit,
	// checkpoint and change detection also recognise the projection by content hash.
	_ = os.MkdirAll(filepath.Join(repoDir, ".agents"), 0755)
	_ = os.WriteFile(filepath.Join(repoDir, runtimestate.GoWorkMarker), []byte(runtimestate.HashBytes(projected)), 0644)
	cmd := exec.Command("git", "update-index", "--skip-worktree", "go.work")
	cmd.Dir = repoDir
	return cmd.Run()
}

// restoreGoWork undoes SyncGoWork, bringing back the committed go.work.
func restoreGoWork(repoDir string) {
	_ = os.Remove(filepath.Join(repoDir, runtimestate.GoWorkMarker))
	cmd := exec.Command("git", "update-index", "--no-skip-worktree", "go.work")
	cmd.Dir = repoDir
	if cmd.Run() != nil {
		return
	}
	co := exec.Command("git", "checkout", "HEAD", "--", "go.work")
	co.Dir = repoDir
	_ = co.Run()
}

func loadGraphFromGit(repoDir string) (*graph.DependencyGraph, error) {
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
