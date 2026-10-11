package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/antigravity/monoflow/pkg/events"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/release"
	"github.com/antigravity/monoflow/pkg/verify"
	"github.com/antigravity/monoflow/pkg/version"
)

func handleVersion(rootDir string) {
	sub := "list"
	if len(os.Args) > 2 {
		sub = os.Args[2]
	}

	switch sub {
	case "list", "all":
		rep, err := version.ListPackageVersions(rootDir)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(data))

	case "bump":
		refuseSharedRoot(rootDir, "version bump")
		if len(os.Args) < 5 {
			fmt.Println(`{"error": "usage: monoflow version bump <pkg> <patch|minor|major> [--cascade]"}`)
			os.Exit(1)
		}
		pkgName := os.Args[3]
		bumpType := os.Args[4]
		cascade := false
		for _, arg := range os.Args[5:] {
			if arg == "--cascade" {
				cascade = true
			}
		}
		res, err := version.BumpPackageVersion(rootDir, pkgName, bumpType, cascade)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "edges", "releases", "release", "pin", "unpin", "upgrades", "upgrade", "suggest":
		handleReleases(rootDir, sub)

	case "apply":
		res, err := version.ApplyChangesets(rootDir)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "check", "verify":
		rep, err := version.CheckVersionConsistency(rootDir)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(data))
		if rep.Status == "error" {
			os.Exit(1)
		}

	default:
		rep, err := version.ListPackageVersions(rootDir)
		if err != nil {
			fmt.Printf(`{"version": %q, "error": %q}`+"\n", version.EngineVersion, err.Error())
			return
		}
		data, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(data))
	}
}

// handleReleases implements internal package releases and per-consumer pinning.
func handleReleases(rootDir, sub string) {
	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil {
		fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
		os.Exit(1)
	}
	args := os.Args[3:]
	flag := func(name string) string {
		for _, a := range args {
			if strings.HasPrefix(a, "--"+name+"=") {
				return strings.TrimPrefix(a, "--"+name+"=")
			}
		}
		return ""
	}
	var pos []string
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
		}
	}
	out := func(v any) {
		data, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(data))
	}
	fail := func(err error) {
		fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
		os.Exit(1)
	}
	need := func(n int, usage string) {
		if len(pos) < n {
			fmt.Printf(`{"error": "usage: monoflow version %s"}`+"\n", usage)
			os.Exit(1)
		}
	}

	switch sub {
	case "edges":
		edges := g.ScanEdges()
		var problems int
		for _, e := range edges {
			if e.Problem != "" {
				problems++
			}
		}
		out(map[string]any{"edges": edges, "problems": problems})
		if problems > 0 {
			os.Exit(1)
		}

	case "releases":
		res := map[string]any{}
		for _, p := range g.GetAllPackages() {
			if len(pos) > 0 && p.Name != pos[0] {
				continue
			}
			if v := release.List(rootDir, p.Name); len(v) > 0 || len(pos) > 0 {
				res[p.Name] = map[string]any{"current": p.Version, "released": v}
			}
		}
		out(res)

	case "release":
		need(1, "release <pkg>")
		tag, err := release.Create(rootDir, g, pos[0])
		if err != nil {
			fail(err)
		}
		pkg, _ := g.GetPackage(pos[0])
		notified := notifyConsumers(rootDir, g, pkg)
		out(map[string]any{"status": "released", "tag": tag, "consumers_notified": notified,
			"next": "push the tag with 'monoflow push' so CI and other clones can materialise it"})

	case "pin", "unpin":
		refuseSharedRoot(rootDir, "version "+sub)
		need(2, sub+" <consumer> <dependency> [--version=<v>]")
		ver := ""
		if sub == "pin" {
			if ver = flag("version"); ver == "" {
				if dep, ok := g.GetPackage(pos[1]); ok {
					ver = release.Latest(rootDir, dep.Name)
				}
			}
			if ver == "" {
				fail(fmt.Errorf("%s has no release to pin to; release it first (monoflow version release %s)", pos[1], pos[1]))
			}
			if _, err := release.Materialize(rootDir, g, pos[1], ver); err != nil {
				fail(err)
			}
		}
		file, err := release.Repoint(rootDir, g, pos[0], pos[1], ver)
		if err != nil {
			fail(err)
		}
		if g2, err := graph.LoadOrDiscover(rootDir); err == nil {
			release.Prune(rootDir, g2)
		}
		mode := "source (live code)"
		if ver != "" {
			mode = "pinned@" + ver
		}
		out(map[string]any{"status": sub + "ned", "consumer": pos[0], "dependency": pos[1], "mode": mode, "file": file,
			"next": "monoflow verify, then monoflow ship -m \"<msg>\""})

	case "upgrades":
		consumer := ""
		if len(pos) > 0 {
			consumer = pos[0]
		}
		ups := release.Available(rootDir, g, consumer)
		out(map[string]any{"upgrades": ups, "count": len(ups)})

	case "upgrade":
		refuseSharedRoot(rootDir, "version upgrade")
		need(2, "upgrade <consumer> <dependency> [--to=<v>]")
		to := flag("to")
		if to == "" {
			for _, u := range release.Available(rootDir, g, pos[0]) {
				if u.Dependency == pos[1] && u.Kind == "compatible" {
					to = u.To
				}
			}
		}
		if to == "" {
			fail(fmt.Errorf("no compatible release of %s newer than the pinned one; pass --to=<version> for a breaking upgrade", pos[1]))
		}
		if _, err := release.Materialize(rootDir, g, pos[1], to); err != nil {
			fail(err)
		}
		if _, err := release.Repoint(rootDir, g, pos[0], pos[1], to); err != nil {
			fail(err)
		}
		if g2, err := graph.LoadOrDiscover(rootDir); err == nil {
			release.Prune(rootDir, g2)
		}
		// The consumer's own checks are its "CI" for the upgrade.
		g2, _ := graph.LoadOrDiscover(rootDir)
		cons, _ := g2.GetPackage(pos[0])
		res := verify.NewRunner(rootDir).VerifyAffected(g2, []string{filepath.Join(cons.Path, "__upgrade__")}, verify.VerifyOptions{})
		out(map[string]any{"status": "upgraded", "consumer": pos[0], "dependency": pos[1], "to": to, "verify": res.Status, "results": failedResults(res),
			"next": "fix any failures, then monoflow commit"})
		if res.Status != "passed" {
			os.Exit(1)
		}

	case "suggest":
		var targets []string
		if len(pos) > 0 {
			targets = pos
		} else {
			for _, f := range changedFilesSince(rootDir, firstNonEmpty(flag("base"), "main")) {
				if p := g.MapFileToPackage(f); p != nil && !graph.IsNonCodeFile(f) && !containsStr(targets, p.Name) {
					targets = append(targets, p.Name)
				}
			}
		}
		var res []*release.Suggestion
		for _, t := range targets {
			s, err := release.Suggest(rootDir, g, t, firstNonEmpty(flag("base"), "main"))
			if err != nil {
				fail(err)
			}
			res = append(res, s)
		}
		out(map[string]any{"suggestions": res})
	}
}

// notifyConsumers emits a DEPENDENCY_RELEASED event per consumer, classified for it.
func notifyConsumers(rootDir string, g *graph.DependencyGraph, pkg *graph.PackageNode) int {
	em := events.NewEventManager(rootDir)
	n := 0
	for _, e := range g.ScanEdges() {
		if e.Dependency != pkg.Name {
			continue
		}
		evt := &events.Event{Type: events.EventDependencyReleased, Payload: map[string]any{
			"dependency": pkg.Name, "version": pkg.Version, "consumer": e.Consumer, "mode": e.Mode}}
		if e.Mode == graph.EdgeSource {
			evt.Summary = fmt.Sprintf("%s@%s released; %s already builds against the live code (no action)", pkg.Name, pkg.Version, e.Consumer)
		} else {
			kind := "compatible"
			for _, u := range release.Available(rootDir, g, e.Consumer) {
				if u.Dependency == pkg.Name && u.To == pkg.Version {
					kind = u.Kind
					evt.Payload["impact"] = u.Impact
					evt.SuggestedTask = u.Command
				}
			}
			evt.RequiresAction = kind == "breaking"
			evt.Summary = fmt.Sprintf("%s@%s released (%s for %s, pinned at %s)", pkg.Name, pkg.Version, kind, e.Consumer, e.Constraint)
		}
		if em.Emit(evt) == nil {
			n++
		}
	}
	return n
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
