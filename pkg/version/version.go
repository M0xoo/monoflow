package version

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/release"
	"github.com/antigravity/monoflow/pkg/semver"
	"github.com/antigravity/monoflow/pkg/task"
)

// Current MonoFlow engine version
const EngineVersion = "1.0.0"

// SemVer is a semantic version; see pkg/semver.
type SemVer = semver.SemVer

// ParseSemVer parses a SemVer string, ignoring an optional leading 'v'.
func ParseSemVer(v string) (*SemVer, error) { return semver.Parse(v) }

// PackageVersionInfo records details about a package's version and dependencies.
type PackageVersionInfo struct {
	Name              string   `json:"name"`
	Version           string   `json:"version"`
	Path              string   `json:"path"`
	Language          string   `json:"language"`
	Dependencies      []string `json:"dependencies"`
	PendingChangesets []string `json:"pending_changesets,omitempty"`
}

// VersionReport details the version status across all packages in the monorepo.
type VersionReport struct {
	MonoFlowVersion   string               `json:"monoflow_version"`
	Engine            string               `json:"engine"`
	TotalPackages     int                  `json:"total_packages"`
	Packages          []PackageVersionInfo `json:"packages"`
	PendingChangesets int                  `json:"pending_changesets_count"`
}

// BumpResult records the outcome of a version bump operation.
type BumpResult struct {
	Package            string           `json:"package"`
	OldVersion         string           `json:"old_version"`
	NewVersion         string           `json:"new_version"`
	BumpType           string           `json:"bump_type"`
	FilesModified      []string         `json:"files_modified"`
	CascadedDependents []string         `json:"cascaded_dependents,omitempty"`
	DurationMs         float64          `json:"duration_ms"`
	Status             string           `json:"status"`
	Consumers          []ConsumerImpact `json:"consumers,omitempty"`
}

// ApplyResult details the outcome of applying pending changesets.
type ApplyResult struct {
	Status            string       `json:"status"`
	AppliedChangesets int          `json:"applied_changesets"`
	BumpedPackages    []BumpResult `json:"bumped_packages"`
	DurationMs        float64      `json:"duration_ms"`
}

// VersionViolation records a version inconsistency or missing constraint.
type VersionViolation struct {
	ConsumerPackage string `json:"consumer_package"`
	Dependency      string `json:"dependency"`
	Severity        string `json:"severity"` // "ERROR", "WARNING"
	Message         string `json:"message"`
}

// ConsistencyReport validates version health across the monorepo and active subsets.
type ConsistencyReport struct {
	Status              string             `json:"status"` // "consistent", "warning", "error"
	IsSubsetActive      bool               `json:"is_subset_active"`
	PackagesChecked     int                `json:"packages_checked"`
	DependenciesChecked int                `json:"dependencies_checked"`
	Violations          []VersionViolation `json:"violations,omitempty"`
	Verdict             string             `json:"verdict"`
	DurationMs          float64            `json:"duration_ms"`
}

// ListPackageVersions inspects all packages in the monorepo and returns a VersionReport.
func ListPackageVersions(rootDir string) (*VersionReport, error) {
	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load monorepo graph: %w", err)
	}

	pkgs := g.GetAllPackages()
	pendingMap, _ := loadPendingChangesets(rootDir)

	var pkgInfos []PackageVersionInfo
	for _, p := range pkgs {
		ver := p.Version
		if ver == "" {
			ver = "1.0.0"
		}

		changesets := pendingMap[p.Name]
		if changesets == nil {
			changesets = []string{}
		}

		pkgInfos = append(pkgInfos, PackageVersionInfo{
			Name:              p.Name,
			Version:           ver,
			Path:              p.Path,
			Language:          string(p.Language),
			Dependencies:      p.Dependencies,
			PendingChangesets: changesets,
		})
	}

	totalPending := 0
	for _, csList := range pendingMap {
		totalPending += len(csList)
	}

	return &VersionReport{
		MonoFlowVersion:   EngineVersion,
		Engine:            "MonoFlow (Agent-Native Monorepo & Ultra-Fast CI Engine)",
		TotalPackages:     len(pkgInfos),
		Packages:          pkgInfos,
		PendingChangesets: totalPending,
	}, nil
}

// BumpPackageVersion updates the SemVer of a package and cascades to downstream dependents.
func BumpPackageVersion(rootDir, pkgName, bumpType string, cascade bool) (*BumpResult, error) {
	start := time.Now()

	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load monorepo graph: %w", err)
	}

	targetPkg, exists := g.GetPackage(pkgName)
	if !exists {
		// Try matching without scope or path
		for _, p := range g.GetAllPackages() {
			if p.Name == pkgName || filepath.Base(p.Path) == pkgName {
				targetPkg = p
				exists = true
				break
			}
		}
	}
	if !exists {
		return nil, fmt.Errorf("package %q not found in monorepo graph", pkgName)
	}

	oldVerStr := targetPkg.Version
	if oldVerStr == "" {
		oldVerStr = "1.0.0"
	}

	semver, err := ParseSemVer(oldVerStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse current version %q of %s: %w", oldVerStr, pkgName, err)
	}

	if err := semver.Bump(bumpType); err != nil {
		return nil, err
	}
	newVerStr := semver.String()

	var modifiedFiles []string

	// 1. Update package native manifest file
	pkgAbsDir := filepath.Join(rootDir, targetPkg.Path)
	manifestModified, err := updatePackageManifestVersion(pkgAbsDir, newVerStr)
	if err == nil && manifestModified != "" {
		rel, _ := filepath.Rel(rootDir, manifestModified)
		modifiedFiles = append(modifiedFiles, rel)
	}

	// 2. Update .agents/monoflow.config.json if it exists
	configPath := filepath.Join(rootDir, ".agents", "monoflow.config.json")
	if _, err := os.Stat(configPath); err == nil {
		targetPkg.Version = newVerStr
		_ = g.SaveConfig(configPath)
		modifiedFiles = append(modifiedFiles, ".agents/monoflow.config.json")
	}

	// 3. Consumers are never rewritten implicitly: a major bump used to force every
	// consumer onto the breaking version. Report how each consumer is affected instead;
	// ranges change only through an explicit --cascade (or the consumer's own upgrade).
	var cascaded []string
	var impact []ConsumerImpact
	for _, e := range g.ScanEdges() {
		if e.Dependency != targetPkg.Name {
			continue
		}
		ci := ConsumerImpact{Consumer: e.Consumer, Mode: e.Mode, Constraint: e.Constraint}
		switch {
		case e.Mode == graph.EdgePinned:
			ci.Action = "none: stays on its pinned release; it is notified of the new release and upgrades at its own pace"
		case e.Constraint != "" && !satisfiesNew(e, newVerStr):
			ci.Action = fmt.Sprintf("BLOCKED: its range %q excludes %s; update the range in the same change or pin it to the previous release (monoflow version pin %s %s)", e.Constraint, newVerStr, e.Consumer, targetPkg.Name)
		case strings.ToLower(bumpType) == "major":
			ci.Action = "builds against the live code: migrate it in this change (verify --affected proves it) or evolve the API expand → contract"
		default:
			ci.Action = "compatible: builds against the live code, verified with --affected"
		}
		impact = append(impact, ci)
	}
	if cascade {
		downstream := g.GetDownstream(targetPkg.Name)
		for _, down := range downstream {
			downPkg, ok := g.GetPackage(down)
			if !ok {
				continue
			}
			downDir := filepath.Join(rootDir, downPkg.Path)
			if updated, err := updateDownstreamDependencyVersion(downDir, targetPkg.Name, newVerStr); err == nil && updated != "" {
				rel, _ := filepath.Rel(rootDir, updated)
				modifiedFiles = append(modifiedFiles, rel)
				cascaded = append(cascaded, downPkg.Name)
			}
		}
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0

	return &BumpResult{
		Package:            targetPkg.Name,
		OldVersion:         oldVerStr,
		NewVersion:         newVerStr,
		BumpType:           strings.ToLower(bumpType),
		FilesModified:      modifiedFiles,
		CascadedDependents: cascaded,
		Consumers:          impact,
		DurationMs:         durMs,
		Status:             "bumped",
	}, nil
}

// ConsumerImpact explains what a version bump means for one consumer.
type ConsumerImpact struct {
	Consumer   string `json:"consumer"`
	Mode       string `json:"mode"`
	Constraint string `json:"constraint,omitempty"`
	Action     string `json:"action"`
}

func satisfiesNew(e graph.Edge, v string) bool {
	eco := semver.Ecosystem(e.Ecosystem)
	ok, err := semver.Satisfies(eco, e.Constraint, v)
	return err != nil || ok
}

// ApplyChangesets reads all pending changesets, applies appropriate SemVer bumps, and archives them.
func ApplyChangesets(rootDir string) (*ApplyResult, error) {
	start := time.Now()

	changesetsDir := filepath.Join(rootDir, ".agents", "changesets")
	entries, err := os.ReadDir(changesetsDir)
	if err != nil || len(entries) == 0 {
		return &ApplyResult{
			Status:            "no_pending_changesets",
			AppliedChangesets: 0,
			BumpedPackages:    []BumpResult{},
			DurationMs:        float64(time.Since(start).Microseconds()) / 1000.0,
		}, nil
	}

	// Map of package -> highest bump type
	pkgBumps := make(map[string]string)
	var changesetFiles []string

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(changesetsDir, e.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var cs task.Changeset
		if err := json.Unmarshal(data, &cs); err != nil {
			continue
		}

		changesetFiles = append(changesetFiles, filePath)
		currentBump := pkgBumps[cs.Package]
		pkgBumps[cs.Package] = resolveHighestBump(currentBump, cs.BumpType)
	}

	if len(pkgBumps) == 0 {
		return &ApplyResult{
			Status:            "no_valid_changesets",
			AppliedChangesets: 0,
			BumpedPackages:    []BumpResult{},
			DurationMs:        float64(time.Since(start).Microseconds()) / 1000.0,
		}, nil
	}

	var results []BumpResult
	for pkg, bumpType := range pkgBumps {
		res, err := BumpPackageVersion(rootDir, pkg, bumpType, true)
		if err != nil {
			return nil, fmt.Errorf("failed to bump package %q: %w", pkg, err)
		}
		results = append(results, *res)
	}

	// Archive applied changesets
	appliedDir := filepath.Join(changesetsDir, "applied")
	_ = os.MkdirAll(appliedDir, 0755)
	for _, f := range changesetFiles {
		dest := filepath.Join(appliedDir, filepath.Base(f))
		_ = os.Rename(f, dest)
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	return &ApplyResult{
		Status:            "applied",
		AppliedChangesets: len(changesetFiles),
		BumpedPackages:    results,
		DurationMs:        durMs,
	}, nil
}

// CheckVersionConsistency validates version syntax, dependency compatibility, and active subset safety.
func CheckVersionConsistency(rootDir string) (*ConsistencyReport, error) {
	start := time.Now()

	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load monorepo graph: %w", err)
	}

	pkgs := g.GetAllPackages()
	pkgMap := make(map[string]*graph.PackageNode)
	for _, p := range pkgs {
		pkgMap[p.Name] = p
	}

	// Check if subset is active
	subsetMetaPath := filepath.Join(rootDir, ".agents", "subset.json")
	isSubsetActive := false
	var activePkgs []string
	if data, err := os.ReadFile(subsetMetaPath); err == nil {
		var subMeta struct {
			IncludedPackages []string `json:"included_packages"`
		}
		if err := json.Unmarshal(data, &subMeta); err == nil && len(subMeta.IncludedPackages) > 0 {
			isSubsetActive = true
			activePkgs = subMeta.IncludedPackages
		}
	}

	var violations []VersionViolation
	depsChecked := 0

	for _, p := range pkgs {
		ver := p.Version
		if ver == "" {
			ver = "1.0.0"
		}
		if _, err := ParseSemVer(ver); err != nil {
			violations = append(violations, VersionViolation{
				ConsumerPackage: p.Name,
				Dependency:      "",
				Severity:        "ERROR",
				Message:         fmt.Sprintf("Invalid SemVer version %q for package %s: %v", ver, p.Name, err),
			})
		}

		for _, dep := range p.Dependencies {
			depsChecked++
			depPkg, exists := pkgMap[dep]
			if !exists {
				// May be an external library, skip internal checks
				continue
			}

			depVer := depPkg.Version
			if depVer == "" {
				depVer = "1.0.0"
			}
			if _, err := ParseSemVer(depVer); err != nil {
				violations = append(violations, VersionViolation{
					ConsumerPackage: p.Name,
					Dependency:      dep,
					Severity:        "ERROR",
					Message:         fmt.Sprintf("Dependency %s has invalid SemVer %q", dep, depVer),
				})
			}
		}
	}

	for _, f := range release.Tampered(rootDir, g) {
		violations = append(violations, VersionViolation{Severity: "ERROR", Message: "vendored release snapshot modified: " + f})
	}

	// Internal edges: a range that excludes the version it resolves to breaks install/build.
	for _, e := range g.ScanEdges() {
		if e.Problem != "" {
			violations = append(violations, VersionViolation{
				ConsumerPackage: e.Consumer,
				Dependency:      e.Dependency,
				Severity:        "ERROR",
				Message:         e.Problem,
			})
		}
	}

	// In subset mode, check that active packages don't depend on omitted packages with incompatible versions
	if isSubsetActive {
		activeSet := make(map[string]bool)
		for _, a := range activePkgs {
			activeSet[a] = true
		}

		for _, active := range activePkgs {
			p := pkgMap[active]
			if p == nil {
				continue
			}
			for _, dep := range p.Dependencies {
				if !activeSet[dep] {
					// Omitted upstream dependency in subset!
					depPkg := pkgMap[dep]
					depVer := "1.0.0"
					if depPkg != nil && depPkg.Version != "" {
						depVer = depPkg.Version
					}
					violations = append(violations, VersionViolation{
						ConsumerPackage: active,
						Dependency:      dep,
						Severity:        "WARNING",
						Message:         fmt.Sprintf("Active package %q depends on omitted package %q (v%s). If this dependency is required for local build/test, run 'monoflow subset add %s'.", active, dep, depVer, dep),
					})
				}
			}
		}
	}

	status := "consistent"
	verdict := "All packages satisfy SemVer and all internal dependency versions are compatible."
	if len(violations) > 0 {
		hasError := false
		for _, v := range violations {
			if v.Severity == "ERROR" {
				hasError = true
				break
			}
		}
		if hasError {
			status = "error"
			verdict = fmt.Sprintf("Found %d version violation(s) across packages.", len(violations))
		} else {
			status = "warning"
			verdict = fmt.Sprintf("Found %d warning(s) regarding omitted dependencies in active subset.", len(violations))
		}
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0

	return &ConsistencyReport{
		Status:              status,
		IsSubsetActive:      isSubsetActive,
		PackagesChecked:     len(pkgs),
		DependenciesChecked: depsChecked,
		Violations:          violations,
		Verdict:             verdict,
		DurationMs:          durMs,
	}, nil
}

func loadPendingChangesets(rootDir string) (map[string][]string, error) {
	dir := filepath.Join(rootDir, ".agents", "changesets")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return map[string][]string{}, nil
	}

	res := make(map[string][]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var cs task.Changeset
		if err := json.Unmarshal(data, &cs); err == nil && cs.Package != "" {
			summary := fmt.Sprintf("[%s] %s (%s)", strings.ToUpper(cs.BumpType), cs.Summary, cs.TaskID)
			res[cs.Package] = append(res[cs.Package], summary)
		}
	}
	return res, nil
}

func resolveHighestBump(current, next string) string {
	score := func(b string) int {
		switch strings.ToLower(b) {
		case "major":
			return 3
		case "minor":
			return 2
		case "patch":
			return 1
		default:
			return 0
		}
	}
	if score(next) > score(current) {
		return strings.ToLower(next)
	}
	return strings.ToLower(current)
}

func updatePackageManifestVersion(pkgDir, newVersion string) (string, error) {
	// 1. package.json
	pkgJsonPath := filepath.Join(pkgDir, "package.json")
	if data, err := os.ReadFile(pkgJsonPath); err == nil {
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err == nil {
			raw["version"] = newVersion
			out, _ := json.MarshalIndent(raw, "", "  ")
			_ = os.WriteFile(pkgJsonPath, append(out, '\n'), 0644)
			return pkgJsonPath, nil
		}
	}

	// 2. Cargo.toml ([package] only — [dependencies.x] tables also have version keys)
	cargoPath := filepath.Join(pkgDir, "Cargo.toml")
	if data, err := os.ReadFile(cargoPath); err == nil {
		_ = os.WriteFile(cargoPath, []byte(setTomlVersion(string(data), []string{"package"}, newVersion)), 0644)
		return cargoPath, nil
	}

	// 3. pyproject.toml ([project] or [tool.poetry])
	pyPath := filepath.Join(pkgDir, "pyproject.toml")
	if data, err := os.ReadFile(pyPath); err == nil {
		_ = os.WriteFile(pyPath, []byte(setTomlVersion(string(data), []string{"project", "tool.poetry"}, newVersion)), 0644)
		return pyPath, nil
	}

	return "", nil
}

// setTomlVersion rewrites `version = "..."` only inside the given sections.
func setTomlVersion(content string, sections []string, newVersion string) string {
	want := map[string]bool{}
	for _, s := range sections {
		want[s] = true
	}
	lines := strings.Split(content, "\n")
	cur := ""
	re := regexp.MustCompile(`^(\s*version\s*=\s*)["'][^"']*["']`)
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") && !strings.HasPrefix(t, "[[") {
			cur = strings.TrimSpace(strings.Trim(t, "[]"))
			continue
		}
		if want[cur] && re.MatchString(l) {
			lines[i] = re.ReplaceAllString(l, `${1}"`+newVersion+`"`)
		}
	}
	return strings.Join(lines, "\n")
}

func updateDownstreamDependencyVersion(pkgDir, depName, newVersion string) (string, error) {
	pkgJsonPath := filepath.Join(pkgDir, "package.json")
	if data, err := os.ReadFile(pkgJsonPath); err == nil {
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err == nil {
			modified := false
			for _, sec := range []string{"dependencies", "devDependencies", "peerDependencies"} {
				if deps, ok := raw[sec].(map[string]any); ok {
					for k := range deps {
						if k == depName || strings.HasSuffix(k, "/"+depName) {
							cur, _ := deps[k].(string)
							switch {
							case strings.HasPrefix(cur, "file:"), strings.HasPrefix(cur, "link:"), cur == "workspace:*", cur == "*":
								continue // tracks the live package (or a pinned snapshot): nothing to rewrite
							case strings.HasPrefix(cur, "workspace:"):
								deps[k] = "workspace:^" + newVersion
							default:
								deps[k] = "^" + newVersion
							}
							modified = true
						}
					}
				}
			}
			if modified {
				out, _ := json.MarshalIndent(raw, "", "  ")
				_ = os.WriteFile(pkgJsonPath, append(out, '\n'), 0644)
				return pkgJsonPath, nil
			}
		}
	}
	return "", nil
}
