package release

import (
	"fmt"
	"strings"

	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/semver"
)

// Upgrade describes a newer internal release available to a pinned consumer.
type Upgrade struct {
	Consumer   string   `json:"consumer"`
	Dependency string   `json:"dependency"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	Kind       string   `json:"kind"` // "compatible" (same major) or "breaking"
	Impact     []Change `json:"impact,omitempty"`
	Command    string   `json:"command"`
}

// Available lists, for every pinned edge (optionally of one consumer), the newest
// compatible release and the newest breaking release, with the API changes between the
// pinned and the new release that this consumer actually uses.
func Available(root string, g *graph.DependencyGraph, consumer string) []Upgrade {
	var out []Upgrade
	for _, e := range g.ScanEdges() {
		if e.Mode != graph.EdgePinned || (consumer != "" && e.Consumer != consumer) {
			continue
		}
		cur, err := semver.Parse(e.Constraint)
		if err != nil {
			continue
		}
		var compat, breaking string
		for _, v := range List(root, e.Dependency) {
			nv, _ := semver.Parse(v)
			if !less(e.Constraint, v) {
				continue
			}
			if nv.Major == cur.Major && cur.Major > 0 || (cur.Major == 0 && nv.Major == 0 && nv.Minor == cur.Minor) {
				compat = v
			} else {
				breaking = v
			}
		}
		dep, _ := g.GetPackage(e.Dependency)
		cons, _ := g.GetPackage(e.Consumer)
		for _, target := range []struct{ v, kind string }{{compat, "compatible"}, {breaking, "breaking"}} {
			if target.v == "" || dep == nil || cons == nil {
				continue
			}
			u := Upgrade{Consumer: e.Consumer, Dependency: e.Dependency, From: e.Constraint, To: target.v, Kind: target.kind,
				Command: fmt.Sprintf("monoflow version upgrade %s %s --to=%s", e.Consumer, e.Dependency, target.v)}
			before, err1 := SurfaceAt(root, dep, Tag(dep.Name, e.Constraint))
			after, err2 := SurfaceAt(root, dep, Tag(dep.Name, target.v))
			if err1 == nil && err2 == nil {
				var breakingChanges []Change
				for _, c := range Diff(before, after) {
					if c.Kind != "added" {
						breakingChanges = append(breakingChanges, c)
					}
				}
				u.Impact = UsedBy(root, cons, breakingChanges)
			}
			out = append(out, u)
		}
	}
	return out
}

// Suggestion is the bump a changed package needs, derived from its API diff against the
// latest release (or a base revision when it was never released).
type Suggestion struct {
	Package  string   `json:"package"`
	Since    string   `json:"since"`
	Required string   `json:"required_bump"`
	Changes  []Change `json:"api_changes,omitempty"`
	// Consumers that build against the live code and use a removed/changed symbol: they
	// must be migrated in the same change (or the API evolved expand → contract).
	SourceConsumersAffected []string `json:"source_consumers_affected,omitempty"`
}

// Suggest computes the required bump of pkg relative to its latest release (or base rev).
func Suggest(root string, g *graph.DependencyGraph, pkgName, baseRev string) (*Suggestion, error) {
	pkg, ok := g.GetPackage(pkgName)
	if !ok {
		return nil, fmt.Errorf("package %q not found", pkgName)
	}
	since := baseRev
	if latest := Latest(root, pkg.Name); latest != "" {
		since = Tag(pkg.Name, latest)
	}
	if since == "" {
		return nil, fmt.Errorf("%s has no release and no base revision to compare with", pkg.Name)
	}
	before, err := SurfaceAt(root, pkg, since)
	if err != nil {
		return nil, err
	}
	after, err := SurfaceAt(root, pkg, "")
	if err != nil {
		return nil, err
	}
	changes := Diff(before, after)
	diff, _ := git(root, "diff", "--name-only", since, "--", pkg.Path)
	s := &Suggestion{Package: pkg.Name, Since: since, Changes: changes, Required: RequiredBump(changes, strings.TrimSpace(diff) != "")}
	var breaking []Change
	for _, c := range changes {
		if c.Kind != "added" {
			breaking = append(breaking, c)
		}
	}
	if len(breaking) > 0 {
		for _, e := range g.ScanEdges() {
			if e.Dependency == pkg.Name && e.Mode == graph.EdgeSource {
				if cons, ok := g.GetPackage(e.Consumer); ok && len(UsedBy(root, cons, breaking)) > 0 {
					s.SourceConsumersAffected = append(s.SourceConsumersAffected, cons.Name)
				}
			}
		}
	}
	return s, nil
}
