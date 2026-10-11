package release

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/antigravity/monoflow/pkg/graph"
)

// API is a package's exported surface: symbol -> normalised signature.
type API map[string]string

// Change is one difference between two API surfaces.
type Change struct {
	Symbol string `json:"symbol"`
	Kind   string `json:"kind"` // "removed", "changed", "added"
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// Diff compares two surfaces. Removed or changed symbols are breaking.
func Diff(before, after API) []Change {
	var out []Change
	for sym, sig := range before {
		if now, ok := after[sym]; !ok {
			out = append(out, Change{Symbol: sym, Kind: "removed", Before: sig})
		} else if now != sig {
			out = append(out, Change{Symbol: sym, Kind: "changed", Before: sig, After: now})
		}
	}
	for sym, sig := range after {
		if _, ok := before[sym]; !ok {
			out = append(out, Change{Symbol: sym, Kind: "added", After: sig})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

// RequiredBump is the minimum SemVer bump implied by a diff ("major", "minor", "patch", "").
func RequiredBump(changes []Change, sourceChanged bool) string {
	bump := ""
	if sourceChanged {
		bump = "patch"
	}
	for _, c := range changes {
		switch c.Kind {
		case "removed", "changed":
			return "major"
		case "added":
			bump = "minor"
		}
	}
	return bump
}

// SurfaceAt extracts the exported API of pkg at a git revision ("" = working tree).
func SurfaceAt(root string, pkg *graph.PackageNode, rev string) (API, error) {
	files, err := listFiles(root, pkg.Path, rev)
	if err != nil {
		return nil, err
	}
	api := API{}
	kept := files[:0]
	for _, f := range files {
		if !isTestFile(f) && sourceExt.MatchString(f) {
			kept = append(kept, f)
		}
	}
	files = kept
	var atRev map[string][]byte
	if rev != "" {
		atRev = readAtRev(root, rev, files) // one git process, not one per file
	}
	for _, f := range files {
		var src []byte
		if rev == "" {
			src, err = readWorking(root, f)
		} else {
			var ok bool
			if src, ok = atRev[f]; !ok {
				err = fmt.Errorf("missing")
			} else {
				err = nil
			}
		}
		if err != nil {
			continue
		}
		switch pkg.Language {
		case graph.LangGo:
			if strings.HasSuffix(f, ".go") {
				goSurface(api, f, src)
			}
		case graph.LangJavaScript, graph.LangTypeScript:
			if regexp.MustCompile(`\.(m|c)?[jt]sx?$`).MatchString(f) && !strings.HasSuffix(f, ".d.ts") {
				jsSurface(api, f, pkg.Path, string(src))
			}
		case graph.LangPython:
			if strings.HasSuffix(f, ".py") {
				pySurface(api, f, pkg.Path, string(src))
			}
		case graph.LangRust:
			if strings.HasSuffix(f, ".rs") {
				rustSurface(api, string(src))
			}
		}
	}
	return api, nil
}

func listFiles(root, dir, rev string) ([]string, error) {
	var out []byte
	var err error
	if rev == "" {
		out, err = exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "--", dir).Output()
	} else {
		out, err = exec.Command("git", "-C", root, "ls-tree", "-r", "--name-only", rev, "--", dir).Output()
	}
	if err != nil {
		return nil, err
	}
	all := strings.Split(strings.TrimSpace(string(out)), "\n")
	// A nested package (own manifest) is not part of this one: the root Go module of
	// opentelemetry-go would otherwise parse all 29 modules (15 s per API diff).
	base := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(dir)), "/")
	var nested []string
	for _, f := range all {
		d := filepath.ToSlash(filepath.Dir(f))
		if d == base || (base == "." && d == ".") {
			continue
		}
		switch filepath.Base(f) {
		case "go.mod", "package.json", "Cargo.toml", "pyproject.toml", "setup.py":
			nested = append(nested, d+"/")
		}
	}
	var files []string
	for _, f := range all {
		if f == "" || strings.Contains(f, "/testdata/") || strings.Contains(f, "/node_modules/") {
			continue
		}
		inNested := false
		for _, n := range nested {
			if strings.HasPrefix(f, n) {
				inNested = true
				break
			}
		}
		if !inNested {
			files = append(files, f)
		}
	}
	return files, nil
}

var sourceExt = regexp.MustCompile(`\.(go|[cm]?[jt]sx?|py|rs)$`)

// readAtRev reads files at rev with a single `git cat-file --batch`.
func readAtRev(root, rev string, files []string) map[string][]byte {
	out := map[string][]byte{}
	if len(files) == 0 {
		return out
	}
	var in bytes.Buffer
	for _, f := range files {
		in.WriteString(rev + ":" + f + "\n")
	}
	cmd := exec.Command("git", "-C", root, "cat-file", "--batch")
	cmd.Stdin = &in
	data, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, f := range files {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			break
		}
		header := strings.Fields(string(data[:nl]))
		data = data[nl+1:]
		if len(header) < 3 || header[1] != "blob" {
			continue // "<rev>:<f> missing"
		}
		var size int
		fmt.Sscanf(header[2], "%d", &size)
		if size > len(data) {
			break
		}
		out[f] = data[:size]
		data = data[size:]
		if len(data) > 0 && data[0] == '\n' {
			data = data[1:]
		}
	}
	return out
}

var testFileRe = regexp.MustCompile(`(_test\.go|\.(test|spec)\.[cm]?[jt]sx?|(^|/)tests?/|(^|/)test_[^/]*\.py|_test\.py|(^|/)test\.[cm]?js)$|(^|/)tests?/`)

func isTestFile(f string) bool { return testFileRe.MatchString(f) }

func goSurface(api API, file string, src []byte) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil || strings.HasSuffix(f.Name.Name, "_test") || f.Name.Name == "main" {
		return
	}
	render := func(n any) string {
		var b bytes.Buffer
		_ = printer.Fprint(&b, fset, n)
		return strings.Join(strings.Fields(b.String()), " ")
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recv := render(d.Recv.List[0].Type)
				if !ast.IsExported(strings.TrimLeft(regexp.MustCompile(`\[.*\]`).ReplaceAllString(recv, ""), "*")) {
					continue
				}
				name = strings.TrimLeft(recv, "*") + "." + name
			}
			api[name] = "func" + strings.TrimPrefix(render(d.Type), "func")
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					if sp.Name.IsExported() {
						api[sp.Name.Name] = "type " + render(sp.Type)
					}
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						if n.IsExported() {
							sig := strings.ToLower(d.Tok.String())
							if sp.Type != nil {
								sig += " " + render(sp.Type)
							}
							api[n.Name] = sig
						}
					}
				}
			}
		}
	}
}

var (
	jsExportDecl  = regexp.MustCompile(`(?m)^\s*export\s+(?:default\s+)?(?:async\s+)?(function\*?|class|const|let|var|interface|type|enum)\s+([A-Za-z_$][\w$]*)\s*([^{=;]*)`)
	jsExportList  = regexp.MustCompile(`(?m)^\s*export\s*\{([^}]*)\}`)
	cjsExportObj  = regexp.MustCompile(`module\.exports\s*=\s*\{([^}]*)\}`)
	cjsExportProp = regexp.MustCompile(`(?m)(?:module\.)?exports\.([A-Za-z_$][\w$]*)\s*=`)
	jsFuncParams  = regexp.MustCompile(`(?m)function\s+([A-Za-z_$][\w$]*)\s*\(([^)]*)\)`)
	// TS-typed signatures: function f(a: T): R and const f = (a: T): R =>
	tsFuncSig  = regexp.MustCompile(`(?m)^\s*export\s+(?:default\s+)?(?:async\s+)?function\*?\s+([A-Za-z_$][\w$]*)\s*(<[^>(]*>)?\s*\(([^)]*)\)\s*(:\s*[^{;]+)?`)
	tsArrowSig = regexp.MustCompile(`(?m)^\s*export\s+const\s+([A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s+)?(<[^>(]*>)?\s*\(([^)]*)\)\s*(:\s*[^=]+)?=>`)
)

// jsSurface records exports of a module (keyed module-relative so different entry
// points don't collide). Function signatures are parameter counts, since JS has no types.
var tsInterfaceBody = regexp.MustCompile(`(?s)export\s+interface\s+([A-Za-z_$][\w$]*)[^{]*\{([^}]*)\}`)

func jsSurface(api API, file, pkgDir, src string) {
	mod := strings.TrimPrefix(file, pkgDir+"/")
	params := map[string]string{}
	for _, m := range jsFuncParams.FindAllStringSubmatch(src, -1) {
		params[m[1]] = fmt.Sprintf("(%d params)", paramCount(m[2]))
	}
	add := func(name, kind string) {
		sig := kind
		if p, ok := params[name]; ok {
			sig += " " + p
		}
		api[mod+":"+name] = sig
	}
	for _, m := range jsExportDecl.FindAllStringSubmatch(src, -1) {
		sig := m[1]
		if m[1] == "interface" || m[1] == "type" {
			sig += " " + strings.Join(strings.Fields(m[3]), " ")
		}
		add(m[2], sig)
	}
	for _, m := range tsInterfaceBody.FindAllStringSubmatch(src, -1) {
		api[strings.TrimPrefix(file, pkgDir+"/")+":"+m[1]] = "interface {" + strings.Join(strings.Fields(m[2]), " ") + "}"
	}
	// Typed signatures (TypeScript) replace parameter counts: a changed parameter or return
	// type is a breaking change even when the arity is the same.
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	for _, re := range []*regexp.Regexp{tsFuncSig, tsArrowSig} {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			if strings.Contains(m[3], ":") || m[4] != "" {
				api[mod+":"+m[1]] = "function" + norm(m[2]) + "(" + norm(m[3]) + ")" + norm(m[4])
			}
		}
	}
	for _, re := range []*regexp.Regexp{jsExportList, cjsExportObj} {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			for _, part := range strings.Split(m[1], ",") {
				name := strings.TrimSpace(part)
				if i := strings.Index(name, " as "); i >= 0 {
					name = strings.TrimSpace(name[i+4:])
				}
				if i := strings.Index(name, ":"); i >= 0 {
					name = strings.TrimSpace(name[:i])
				}
				if name != "" && !strings.HasPrefix(name, "...") {
					add(name, "export")
				}
			}
		}
	}
	for _, m := range cjsExportProp.FindAllStringSubmatch(src, -1) {
		add(m[1], "export")
	}
}

func paramCount(p string) int {
	required := 0
	for _, part := range strings.Split(p, ",") {
		part = strings.TrimSpace(part)
		if part != "" && !strings.Contains(part, "=") && !strings.HasPrefix(part, "...") && !strings.HasSuffix(strings.Fields(part)[0], "?") {
			required++
		}
	}
	return required
}

var (
	pyDef = regexp.MustCompile(`(?m)^(def|async def|class)\s+([A-Za-z]\w*)\s*(\([^)]*\))?`)
	pyAll = regexp.MustCompile(`(?s)__all__\s*=\s*[\[(](.*?)[\])]`)
)

func pySurface(api API, file, pkgDir, src string) {
	mod := strings.TrimSuffix(strings.TrimPrefix(file, pkgDir+"/"), ".py")
	if strings.Contains("/"+mod, "/_") && !strings.HasSuffix(mod, "__init__") {
		return // private module
	}
	if m := pyAll.FindStringSubmatch(src); m != nil {
		for _, q := range regexp.MustCompile(`["'](\w+)["']`).FindAllStringSubmatch(m[1], -1) {
			api[mod+":"+q[1]] = "export"
		}
	}
	for _, m := range pyDef.FindAllStringSubmatch(src, -1) {
		sig := m[1]
		if m[1] != "class" {
			sig += " " + strings.Join(strings.Fields(m[3]), " ")
		}
		api[mod+":"+m[2]] = sig
	}
}

var rustPub = regexp.MustCompile(`(?m)^\s*pub\s+(?:async\s+)?(fn|struct|enum|trait|type|const|static|mod)\s+([A-Za-z_]\w*)([^{;]*)`)

func rustSurface(api API, src string) {
	for _, m := range rustPub.FindAllStringSubmatch(src, -1) {
		sig := m[1]
		if m[1] == "fn" {
			sig += strings.Join(strings.Fields(m[3]), " ")
		}
		api[m[2]] = sig
	}
}

// UsedBy returns the changed symbols that consumer source code references (by name), so a
// consumer sees only the breaking changes that concern it.
func UsedBy(root string, consumer *graph.PackageNode, changes []Change) []Change {
	files, err := listFiles(root, consumer.Path, "")
	if err != nil {
		return changes
	}
	var corpus strings.Builder
	for _, f := range files {
		if b, err := readWorking(root, f); err == nil {
			corpus.Write(b)
			corpus.WriteByte('\n')
		}
	}
	text := corpus.String()
	var used []Change
	for _, c := range changes {
		name := c.Symbol
		if i := strings.LastIndexAny(name, ":."); i >= 0 {
			name = name[i+1:]
		}
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(text) {
			used = append(used, c)
		}
	}
	return used
}

func readWorking(root, f string) ([]byte, error) {
	return os.ReadFile(filepath.Join(root, f))
}
