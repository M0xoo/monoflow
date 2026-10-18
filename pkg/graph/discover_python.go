package graph

import (
	"path/filepath"
	"regexp"
	"strings"
)

// discoverPythonPackages registers Python projects (pyproject.toml or setup.py at any
// depth) and links PEP 621 / Poetry / uv / dependency-group requirements on other
// in-repo projects by normalised distribution name.
func (g *DependencyGraph) discoverPythonPackages(files []string) {
	type project struct {
		node *PackageNode
		deps []string
	}
	var projects []project
	byNorm := map[string]string{}

	dirs := map[string]bool{}
	for _, base := range []string{"pyproject.toml", "setup.py"} {
		for _, d := range manifestDirs(files, base) {
			dirs[d] = true
		}
	}
	for dir := range dirs {
		sections := tomlSections(readFileString(filepath.Join(g.RootDir, dir, "pyproject.toml")))
		name := tomlString(sections["project"], "name")
		if name == "" {
			name = tomlString(sections["tool.poetry"], "name")
		}
		if name == "" {
			if dir == "." || !fileExistsAt(filepath.Join(g.RootDir, dir, "setup.py")) {
				continue // tooling-only pyproject (e.g. a repo-root lint config)
			}
			name = filepath.Base(dir)
		}
		byNorm[normalizePyName(name)] = name
		env := pythonEnvFor(g.RootDir, dir)
		projects = append(projects, project{node: &PackageNode{
			Name:         name,
			Path:         dir,
			Language:     LangPython,
			Version:      orDefault(tomlString(sections["project"], "version"), "1.0.0"),
			TestCmd:      env.testCmd(),
			LintCmd:      env.lintCmd(),
			TypecheckCmd: env.typecheckCmd(),
			FixCmd:       env.fixCmd(),
		}, deps: pyRequirements(sections)})
	}
	inRepo := map[string]bool{}
	for _, n := range byNorm {
		inRepo[n] = true
	}
	for _, p := range projects {
		var named []string
		for _, d := range p.deps {
			if n, ok := byNorm[d]; ok {
				named = append(named, n)
			}
		}
		p.node.Dependencies = inRepoDeps(named, inRepo, p.node.Name)
		g.AddPackage(p.node)
	}
}

var pyReqName = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)

// pyRequirements returns the normalised names of every requirement a pyproject declares:
// PEP 621 dependencies and extras, PEP 735 dependency groups, uv dev-dependencies and
// sources, Poetry dependencies and groups.
func pyRequirements(sections map[string][]string) []string {
	reqs := tomlArray(sections["project"], "dependencies")
	reqs = append(reqs, tomlArray(sections["tool.uv"], "dev-dependencies")...)
	for sec, lines := range sections {
		switch {
		case sec == "project.optional-dependencies" || sec == "dependency-groups":
			for _, k := range tomlKeys(lines) {
				reqs = append(reqs, tomlArray(lines, k)...)
			}
		case sec == "tool.poetry.dependencies" || sec == "tool.poetry.dev-dependencies" || strings.HasPrefix(sec, "tool.poetry.group.") || sec == "tool.uv.sources":
			reqs = append(reqs, tomlKeys(lines)...)
		}
	}
	var out []string
	for _, r := range reqs {
		if m := pyReqName.FindStringSubmatch(r); m != nil {
			out = append(out, normalizePyName(m[1]))
		}
	}
	return out
}

// pythonEnv is how a Python project's tools are run: through the project manager that owns
// its environment (uv, Poetry, PDM), or a plain interpreter. A uv-managed repo has no
// `python`, `pytest` or `ruff` on PATH: they live in the project's .venv, which `uv run`
// creates and syncs on demand (also in a fresh worktree or a clean CI checkout).
type pythonEnv struct {
	manager     string   // "uv", "poetry", "pdm" or "" (plain interpreter)
	workspace   bool     // a uv workspace member or root: `uv run --all-packages`
	memberGlobs []string // set on a uv workspace root: its members, left to their own checks

	pytest, ruff, black, mypy bool // declared as a dependency (any group) of the project or its workspace
	ruffConfigured            bool // [tool.ruff] or ruff.toml
	blackConfigured           bool // [tool.black]
	mypyConfigured            bool // [tool.mypy] or mypy.ini
}

// hasSectionPrefix reports a [name] or [name.*] table.
func hasSectionPrefix(sections map[string][]string, name string) bool {
	for sec := range sections {
		if sec == name || strings.HasPrefix(sec, name+".") {
			return true
		}
	}
	return false
}

// pythonEnvFor inspects dir's pyproject and its ancestors up to the repo root: the nearest
// lockfile names the manager, a uv workspace root above (or at) dir makes it a member.
func pythonEnvFor(root, dir string) pythonEnv {
	var env pythonEnv
	var reqs []string
	for d := dir; ; d = parentDir(d) {
		abs := filepath.Join(root, d)
		sections := tomlSections(readFileString(filepath.Join(abs, "pyproject.toml")))
		ws := len(sections["tool.uv.workspace"]) > 0
		if d == dir || ws {
			// Its own requirements, and a uv workspace root's (shared dev tools usually live there).
			reqs = append(reqs, pyRequirements(sections)...)
		}
		if ws && !env.workspace && (env.manager == "" || env.manager == "uv") {
			env.manager, env.workspace = "uv", true
			if d == dir {
				for _, m := range tomlArray(sections["tool.uv.workspace"], "members") {
					if m = strings.TrimPrefix(filepath.ToSlash(m), "./"); m != "" && m != "." {
						env.memberGlobs = append(env.memberGlobs, m)
					}
				}
			}
		}
		if env.manager == "" {
			switch {
			case fileExistsAt(filepath.Join(abs, "uv.lock")), d == dir && hasSectionPrefix(sections, "tool.uv"):
				env.manager = "uv"
			case fileExistsAt(filepath.Join(abs, "poetry.lock")):
				env.manager = "poetry"
			case fileExistsAt(filepath.Join(abs, "pdm.lock")):
				env.manager = "pdm"
			}
		}
		env.ruffConfigured = env.ruffConfigured || hasSectionPrefix(sections, "tool.ruff") || fileExistsAt(filepath.Join(abs, "ruff.toml")) || fileExistsAt(filepath.Join(abs, ".ruff.toml"))
		env.blackConfigured = env.blackConfigured || hasSectionPrefix(sections, "tool.black")
		env.mypyConfigured = env.mypyConfigured || hasSectionPrefix(sections, "tool.mypy") || fileExistsAt(filepath.Join(abs, "mypy.ini")) || fileExistsAt(filepath.Join(abs, ".mypy.ini"))
		if d == "." {
			break
		}
	}
	for _, r := range reqs {
		switch r {
		case "pytest":
			env.pytest = true
		case "ruff":
			env.ruff = true
		case "black":
			env.black = true
		case "mypy":
			env.mypy = true
		}
	}
	return env
}

func parentDir(d string) string {
	if p := filepath.ToSlash(filepath.Dir(d)); p != "" {
		return p
	}
	return "."
}

// run is the prefix running a tool installed in the project's environment.
func (e pythonEnv) run() string {
	switch {
	case e.manager == "uv" && e.workspace:
		// From a member, plain `uv run` installs that member only, without the workspace
		// root's dev group (where pytest and ruff usually are): "Failed to spawn: pytest".
		return "uv run --all-packages "
	case e.manager != "":
		return e.manager + " run "
	}
	return ""
}

// tool runs a tool the project may not declare: from its environment when declared, else an
// ephemeral one with uv (uvx), else whatever is on PATH.
func (e pythonEnv) tool(name string, declared bool) string {
	switch {
	case declared && e.manager != "":
		return e.run() + name
	case e.manager == "uv":
		return "uvx " + name
	}
	return name
}

func (e pythonEnv) testCmd() string {
	var c string
	switch {
	case e.manager == "":
		c = "python3 -m pytest -q"
	case e.pytest || e.manager != "uv":
		c = e.run() + "pytest -q"
	default:
		// Not declared: pytest also runs unittest suites. --with keeps the project's own
		// pytest when it has one.
		c = e.run() + "--with pytest pytest -q"
	}
	for _, m := range e.memberGlobs {
		c += " --ignore-glob=" + shQuote(m) // each member runs its own tests
	}
	return c
}

func (e pythonEnv) lintCmd() string {
	if !e.ruff && !e.ruffConfigured && e.manager != "uv" {
		return "" // no ruff anywhere: a missing linter must not fail every verify
	}
	c := e.tool("ruff", e.ruff) + " check --output-format=concise"
	for _, m := range e.memberGlobs {
		c += " --extend-exclude " + shQuote(m)
	}
	return c + " ."
}

// typecheckCmd runs mypy when the project both declares and configures it (mypy on an
// unconfigured project mostly reports missing stubs).
func (e pythonEnv) typecheckCmd() string {
	if !e.mypy || !e.mypyConfigured {
		return ""
	}
	c := e.tool("mypy", true)
	for _, m := range e.memberGlobs {
		c += " --exclude " + shQuote(strings.ReplaceAll(regexp.QuoteMeta(m), `\*`, "[^/]*"))
	}
	return c + " ."
}

// fixCmd formats with the formatter the project uses (black or ruff), none when it uses neither.
func (e pythonEnv) fixCmd() string {
	switch {
	case e.black || e.blackConfigured:
		return e.tool("black", e.black) + " -q {files:.py}"
	case e.ruff || e.ruffConfigured:
		return e.tool("ruff", e.ruff) + " format {files:.py}"
	}
	return ""
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

var pyNameSep = regexp.MustCompile(`[-_.]+`)

// normalizePyName applies PEP 503 name normalisation.
func normalizePyName(n string) string {
	return pyNameSep.ReplaceAllString(strings.ToLower(n), "-")
}

// UpgradeLegacyPythonCommands replaces the commands configs got before Python environments
// were detected ("python -m pytest -q", "ruff check .", "ruff format"): in a uv repo neither
// python nor ruff is on PATH, so every check failed to start. Commands the repo set itself are
// kept, and so is a legacy one discovery has no replacement for.
func UpgradeLegacyPythonCommands(root string, p *PackageNode) {
	if p.Language != LangPython {
		return
	}
	env := pythonEnvFor(root, p.Path)
	if p.TestCmd == "python -m pytest -q" {
		p.TestCmd = env.testCmd()
	}
	if c := env.lintCmd(); p.LintCmd == "ruff check ." && c != "" {
		p.LintCmd = c
	}
	if c := env.fixCmd(); p.FixCmd == "ruff format {files:.py}" && c != "" {
		p.FixCmd = c
	}
}
