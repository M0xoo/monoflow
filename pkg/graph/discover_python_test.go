package graph

import (
	"strings"
	"testing"
)

// A uv workspace: python, pytest and ruff are not on PATH, they live in the workspace venv,
// with the dev tools declared once in the root's dev group.
func TestPythonCommandsUvWorkspace(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pyproject.toml":               "[project]\nname = \"acme\"\n\n[dependency-groups]\ndev = [\"pytest>=8\", \"ruff>=0.6\"]\n\n[tool.uv.workspace]\nmembers = [\"packages/*\"]\n",
		"uv.lock":                      "version = 1\n",
		"packages/core/pyproject.toml": "[project]\nname = \"acme-core\"\n",
		"packages/api/pyproject.toml":  "[project]\nname = \"acme-api\"\ndependencies = [\"acme-core\"]\n\n[tool.uv.sources]\nacme-core = { workspace = true }\n",
	})
	g := discover(t, root)
	api, _ := g.GetPackage("acme-api")
	if api.TestCmd != "uv run --all-packages pytest -q" || api.LintCmd != "uv run --all-packages ruff check --output-format=concise ." || api.FixCmd != "uv run --all-packages ruff format {files:.py}" {
		t.Errorf("member commands = %q / %q / %q", api.TestCmd, api.LintCmd, api.FixCmd)
	}
	if got := deps(t, g, "acme-api"); len(got) != 1 || got[0] != "acme-core" {
		t.Errorf("acme-api deps = %v", got)
	}
	// The root project leaves its members to their own checks.
	ws, _ := g.GetPackage("acme")
	if !strings.Contains(ws.TestCmd, "--ignore-glob='packages/*'") || !strings.Contains(ws.LintCmd, "--extend-exclude 'packages/*'") {
		t.Errorf("workspace root commands = %q / %q", ws.TestCmd, ws.LintCmd)
	}
}

func TestPythonCommandsByEnvironment(t *testing.T) {
	root := writeTree(t, map[string]string{
		// uv project declaring nothing: ephemeral pytest and ruff, no formatter imposed.
		"bare/pyproject.toml": "[project]\nname = \"bare\"\n",
		"bare/uv.lock":        "",
		// Poetry with black.
		"poet/pyproject.toml": "[tool.poetry]\nname = \"poet\"\n\n[tool.poetry.group.dev.dependencies]\npytest = \"^8\"\nblack = \"^24\"\n",
		"poet/poetry.lock":    "",
		// Plain setuptools project, no linter anywhere: no lint command that cannot run.
		"plain/setup.py": "from setuptools import setup\nsetup()\n",
		// mypy declared and configured.
		"typed/pyproject.toml": "[project]\nname = \"typed\"\n\n[dependency-groups]\ndev = [\"pytest\", \"mypy\"]\n\n[tool.mypy]\nstrict = true\n",
		"typed/uv.lock":        "",
	})
	g := discover(t, root)
	want := map[string][4]string{
		"bare":  {"uv run --with pytest pytest -q", "uvx ruff check --output-format=concise .", "", ""},
		"poet":  {"poetry run pytest -q", "", "", "poetry run black -q {files:.py}"},
		"plain": {"python3 -m pytest -q", "", "", ""},
		"typed": {"uv run pytest -q", "uvx ruff check --output-format=concise .", "uv run mypy .", ""},
	}
	for name, w := range want {
		p, ok := g.GetPackage(name)
		if !ok {
			t.Fatalf("%s not discovered", name)
		}
		if got := [4]string{p.TestCmd, p.LintCmd, p.TypecheckCmd, p.FixCmd}; got != w {
			t.Errorf("%s: got %q, want %q", name, got, w)
		}
	}
}

// Configs written before environment detection ran python/ruff from PATH.
func TestUpgradeLegacyPythonCommands(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pyproject.toml":        "[dependency-groups]\ndev = [\"pytest\", \"ruff\"]\n\n[tool.uv.workspace]\nmembers = [\"libs/*\"]\n",
		"uv.lock":               "",
		"libs/a/pyproject.toml": "[project]\nname = \"a\"\n",
	})
	legacy := PackageNode{Name: "a", Path: "libs/a", Language: LangPython, TestCmd: "python -m pytest -q", LintCmd: "ruff check .", FixCmd: "ruff format {files:.py}"}
	UpgradeLegacyPythonCommands(root, &legacy)
	if legacy.TestCmd != "uv run --all-packages pytest -q" || !strings.HasPrefix(legacy.LintCmd, "uv run --all-packages ruff check") || !strings.HasPrefix(legacy.FixCmd, "uv run --all-packages ruff format") {
		t.Errorf("legacy commands not upgraded: %+v", legacy)
	}
	custom := PackageNode{Name: "a", Path: "libs/a", Language: LangPython, TestCmd: "make test"}
	UpgradeLegacyPythonCommands(root, &custom)
	if custom.TestCmd != "make test" {
		t.Errorf("a command the repo set must be kept, got %q", custom.TestCmd)
	}
}

// uv.lock holds every dependency bump of the workspace: it affects every package.
func TestPythonLockfilesAreGlobal(t *testing.T) {
	for _, f := range []string{"uv.lock", "poetry.lock", "pyproject.toml", ".python-version"} {
		if !IsGlobalFile(f) {
			t.Errorf("%s should be workspace-wide", f)
		}
	}
	if IsGlobalFile("packages/a/pyproject.toml") {
		t.Errorf("a member's pyproject belongs to the member")
	}
}
