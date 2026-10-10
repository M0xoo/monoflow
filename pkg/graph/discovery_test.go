package graph

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func discover(t *testing.T, root string) *DependencyGraph {
	t.Helper()
	g := New(root)
	if err := g.AutoDiscover(); err != nil {
		t.Fatal(err)
	}
	return g
}

func deps(t *testing.T, g *DependencyGraph, name string) []string {
	t.Helper()
	p, ok := g.GetPackage(name)
	if !ok {
		t.Fatalf("package %q not discovered (have %d packages)", name, len(g.GetAllPackages()))
	}
	return p.Dependencies
}

// opentelemetry-go layout: root module + deeply nested modules linked by replace.
func TestDiscoverNestedGoModulesWithRoot(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod":                   "module go.example.io/otel\n\ngo 1.22\n",
		"attribute/key.go":         "package attribute\n",
		"sdk/metric/go.mod":        "module go.example.io/otel/sdk/metric\n\nrequire go.example.io/otel v1.0.0\n\nreplace go.example.io/otel => ../..\n",
		"exporters/otlp/x/go.mod":  "module go.example.io/otel/exporters/otlp/x\n\nrequire (\n\tgo.example.io/otel v1.0.0\n\tgo.example.io/otel/sdk/metric v1.0.0\n)\n\nreplace go.example.io/otel => ../../..\n\nreplace go.example.io/otel/sdk/metric => ../../../sdk/metric\n",
		"bridge/go.mod":            "module go.example.io/otel/bridge\n\nrequire go.example.io/otel v1.0.0\n",
		"internal/testdata/go.mod": "module ignored\n",
	})
	g := discover(t, root)
	if n := len(g.GetAllPackages()); n != 4 {
		t.Fatalf("expected 4 modules (testdata ignored), got %d", n)
	}
	// No replace and no go.work: the bridge builds against a published otel version, so
	// it is pinned and not affected by local changes.
	bridge, _ := g.GetPackage("bridge")
	if len(bridge.Dependencies) != 0 || !reflect.DeepEqual(bridge.PinnedDependencies, []string{"go.example.io/otel"}) {
		t.Errorf("bridge should pin otel, got deps=%v pinned=%v", bridge.Dependencies, bridge.PinnedDependencies)
	}
	if got := deps(t, g, "sdk/metric"); !reflect.DeepEqual(got, []string{"go.example.io/otel"}) {
		t.Errorf("sdk/metric deps = %v", got)
	}
	if got := deps(t, g, "exporters/otlp/x"); len(got) != 2 {
		t.Errorf("exporter deps = %v", got)
	}
	if owner := g.MapFileToPackage("attribute/key.go"); owner == nil || owner.Name != "go.example.io/otel" {
		t.Errorf("root module must own attribute/key.go, got %v", owner)
	}
	if all := g.ComputeAffected([]string{"attribute/key.go"}).AllAffected; len(all) != 3 {
		t.Errorf("root change should affect the 3 source-linked modules (not the pinned bridge), got %v", all)
	}
}

// tokio layout: virtual workspace, path / workspace / table / renamed deps.
func TestDiscoverCargoDependencies(t *testing.T) {
	root := writeTree(t, map[string]string{
		"Cargo.toml":              "[workspace]\nmembers = [\"tokio\", \"tokio-macros\", \"tokio-util\"]\n",
		"tokio/Cargo.toml":        "[package]\nname = \"tokio\"\nversion = \"1.40.0\"\n\n[dependencies]\ntokio-macros = { version = \"2\", path = \"../tokio-macros\", optional = true }\nbytes = \"1\"\n\n[target.'cfg(unix)'.dev-dependencies]\nlibc = \"0.2\"\n",
		"tokio-macros/Cargo.toml": "[package]\nname = \"tokio-macros\"\n\n[dependencies]\nsyn = { version = \"2\", features = [\n  \"full\",\n] }\n",
		"tokio-util/Cargo.toml":   "[package]\nname = \"tokio-util\"\n\n[dependencies.tokio]\npath = \"../tokio\"\n\n[dev-dependencies]\nmacros = { package = \"tokio-macros\", path = \"../tokio-macros\" }\n",
	})
	g := discover(t, root)
	if got := deps(t, g, "tokio"); !reflect.DeepEqual(got, []string{"tokio-macros"}) {
		t.Errorf("tokio deps = %v", got)
	}
	if got := deps(t, g, "tokio-util"); !reflect.DeepEqual(got, []string{"tokio", "tokio-macros"}) {
		t.Errorf("tokio-util deps = %v", got)
	}
	if all := g.ComputeAffected([]string{"tokio-macros/src/lib.rs"}).AllAffected; len(all) != 3 {
		t.Errorf("tokio-macros change should affect 3 crates, got %v", all)
	}
}

// langchain layout: nested projects, PEP 621 + uv sources, tooling-only root pyproject.
func TestDiscoverPythonProjects(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pyproject.toml":                      "[tool.ruff]\nline-length = 100\n",
		"libs/core/pyproject.toml":            "[project]\nname = \"langchain-core\"\ndependencies = [\"pydantic>=2\"]\n",
		"libs/langchain/pyproject.toml":       "[project]\nname = \"langchain\"\ndependencies = [\n  \"langchain-core>=0.3,<1\",\n  \"langchain_text_splitters\",\n]\n\n[tool.uv.sources]\nlangchain-core = { path = \"../core\", editable = true }\nlangchain-text-splitters = { path = \"../text-splitters\" }\n",
		"libs/text-splitters/pyproject.toml":  "[project]\nname = \"langchain-text-splitters\"\n\n[tool.uv.sources]\nlangchain-core = { path = \"../core\", editable = true }\n",
		"libs/partners/openai/pyproject.toml": "[project]\nname = \"langchain-openai\"\n\n[dependency-groups]\ntest = [\"langchain-core\", \"pytest\"]\n",
	})
	g := discover(t, root)
	if n := len(g.GetAllPackages()); n != 4 {
		t.Fatalf("expected 4 projects (tooling root skipped), got %d", n)
	}
	if got := deps(t, g, "langchain"); !reflect.DeepEqual(got, []string{"langchain-core", "langchain-text-splitters"}) {
		t.Errorf("langchain deps = %v", got)
	}
	if got := deps(t, g, "langchain-text-splitters"); !reflect.DeepEqual(got, []string{"langchain-core"}) {
		t.Errorf("text-splitters deps = %v", got)
	}
	// openai requires langchain-core without a local source: it uses the PyPI release.
	if oa, _ := g.GetPackage("langchain-openai"); len(oa.Dependencies) != 0 || !reflect.DeepEqual(oa.PinnedDependencies, []string{"langchain-core"}) {
		t.Errorf("langchain-openai should pin langchain-core, got deps=%v pinned=%v", oa.Dependencies, oa.PinnedDependencies)
	}
}

// changesets layout: pnpm workspace, TS via root tsconfig, root-run vitest/eslint.
func TestDiscoverPnpmWorkspaceWithRootTools(t *testing.T) {
	root := writeTree(t, map[string]string{
		"package.json":                             `{"name":"root","private":true,"scripts":{"test":"vitest","lint":"eslint"},"devDependencies":{"typescript":"5"}}`,
		"pnpm-workspace.yaml":                      "packages:\n  - \"packages/*\"\n  - \"site/**\"\n  - \"!**/__fixtures__/**\"\n",
		"pnpm-lock.yaml":                           "",
		"tsconfig.json":                            "{}",
		"packages/types/package.json":              `{"name":"@cs/types"}`,
		"packages/types/src/index.ts":              "export {}",
		"packages/cli/package.json":                `{"name":"@cs/cli","dependencies":{"@cs/types":"workspace:*"},"scripts":{"test":"node t.js"}}`,
		"packages/cli/__fixtures__/x/package.json": `{"name":"fixture"}`,
		"site/docs/app/package.json":               `{"name":"site-app"}`,
	})
	g := discover(t, root)
	if _, ok := g.GetPackage("fixture"); ok {
		t.Errorf("fixtures must not be packages")
	}
	if _, ok := g.GetPackage("site-app"); !ok {
		t.Errorf("'**' workspace glob must match nested packages")
	}
	types, _ := g.GetPackage("@cs/types")
	if types == nil || types.Language != LangTypeScript {
		t.Fatalf("@cs/types should be TypeScript, got %+v", types)
	}
	if types.TestCmd == "" || types.LintCmd == "" {
		t.Errorf("root vitest/eslint should be scoped to the package, got test=%q lint=%q", types.TestCmd, types.LintCmd)
	}
	if cli, _ := g.GetPackage("@cs/cli"); cli == nil || cli.TestCmd != "npm test" || !reflect.DeepEqual(cli.Dependencies, []string{"@cs/types"}) {
		t.Errorf("@cs/cli = %+v", cli)
	}
}
