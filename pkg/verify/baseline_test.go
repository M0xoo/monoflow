package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/antigravity/monoflow/pkg/cache"
	"github.com/antigravity/monoflow/pkg/graph"
)

// Existing repos are rarely perfectly clean. A failure that main already has must be
// reported as pre-existing, not block the agent; a new failure must still block.
func TestPreexistingFailuresDoNotBlockButNewOnesDo(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	write := func(rel, s string) {
		p := filepath.Join(repo, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		_ = os.WriteFile(p, []byte(s), 0644)
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	// check.sh fails for every file containing "BAD", naming it.
	write("pkg/check.sh", "for f in *.txt; do grep -q BAD $f && echo \"$f: lint error BAD\" && fail=1; done; exit ${fail:-0}\n")
	write("pkg/legacy.txt", "BAD from long ago\n")
	write("pkg/feature.txt", "ok\n")
	git("add", "-A")
	git("commit", "-qm", "base with a pre-existing failure")
	git("checkout", "-qb", "agent/x")

	g := graph.New(repo)
	g.AddPackage(&graph.PackageNode{Name: "pkg", Path: "pkg", Language: graph.LangGeneric, LintCmd: "sh check.sh"})
	g.RebuildEdges()
	r := NewRunner(repo)
	r.SetCacheManager(cache.NewManager(repo, cache.Config{Enabled: true, Dir: filepath.Join(repo, ".agents", "cache")}))

	write("pkg/feature.txt", "still ok\n")
	res := r.VerifyAffected(g, []string{"pkg/feature.txt"}, VerifyOptions{BaseRef: "main"})
	if res.Status != "passed" || len(res.Results[0].Preexisting) == 0 {
		t.Fatalf("pre-existing failure should be reported, not block: %+v", res.Results[0])
	}
	// Unchanged content, same baseline: not re-run (re-running them cost minutes per verify on trpc).
	res = r.VerifyAffected(g, []string{"pkg/feature.txt"}, VerifyOptions{BaseRef: "main"})
	if res.Status != "passed" || !res.Results[0].Cached || len(res.Results[0].Preexisting) == 0 {
		t.Fatalf("preexisting-only result should come from the cache: %+v", res.Results[0])
	}

	write("pkg/feature.txt", "BAD new\n")
	res = r.VerifyAffected(g, []string{"pkg/feature.txt"}, VerifyOptions{BaseRef: "main", NoCache: true})
	if res.Status != "failed" {
		t.Fatalf("a new failure in the same package must block: %+v", res.Results[0])
	}

	write("pkg/feature.txt", "still ok\n")
	res = r.VerifyAffected(g, []string{"pkg/feature.txt"}, VerifyOptions{BaseRef: "main", Strict: true, NoCache: true})
	if res.Status != "failed" {
		t.Fatalf("--strict must block on pre-existing failures too")
	}
}

// A preexisting lint failure stops the check chain; the tests after it must still run, or a
// new test failure hides behind old lint debt (live: opentelemetry-go's unformatted semconv files
// hid a broken root test, so the root module was reported green).
func TestPreexistingLintFailureDoesNotHideNewTestFailure(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	write := func(rel, s string) {
		p := filepath.Join(repo, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		_ = os.WriteFile(p, []byte(s), 0644)
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	write("pkg/legacy.txt", "BAD from long ago\n")
	write("pkg/value.txt", "1\n")
	git("add", "-A")
	git("commit", "-qm", "base with lint debt")
	git("checkout", "-qb", "agent/x")

	g := graph.New(repo)
	g.AddPackage(&graph.PackageNode{Name: "pkg", Path: "pkg", Language: graph.LangGeneric,
		LintCmd: `grep -l BAD *.txt && exit 1; exit 0`,
		TestCmd: `grep -qx 1 value.txt || { echo "value_test.go:3: want 1"; exit 1; }`})
	g.RebuildEdges()
	r := NewRunner(repo)
	r.SetCacheManager(cache.NewManager(repo, cache.Config{Enabled: true, Dir: filepath.Join(repo, ".agents", "cache")}))

	write("pkg/value.txt", "2\n")
	res := r.VerifyAffected(g, []string{"pkg/value.txt"}, VerifyOptions{BaseRef: "main", NoCache: true})
	if res.Status != "failed" || len(res.Results[0].Errors) == 0 || res.Results[0].Errors[0].File != "value_test.go" {
		t.Fatalf("new test failure behind preexisting lint debt must block: %+v", res.Results[0])
	}
	if len(res.Results[0].Preexisting) == 0 {
		t.Fatalf("the old lint failure should still be reported as preexisting: %+v", res.Results[0])
	}

	write("pkg/value.txt", "1\n")
	write("pkg/notes.txt", "unrelated edit\n") // keep a local change, as in the case above
	res = r.VerifyAffected(g, []string{"pkg/notes.txt"}, VerifyOptions{BaseRef: "main", NoCache: true})
	if res.Status != "passed" {
		t.Fatalf("only preexisting lint debt: should pass: %+v", res.Results[0])
	}
}

// A package with one old (flaky) failure and new ones blocks on the new ones only.
func TestSplitByBaselineKeepsOnlyNewFailures(t *testing.T) {
	base := " FAIL  |@trpc/tests| server/memory.test.ts > frees data\nAssertionError: expected [] to be undefined\n ❯ server/memory.test.ts:58:3\n"
	errs := []DiagnosticError{
		{File: "server/memory.test.ts", Line: 58, Code: "test-failure", Message: "[frees data] AssertionError: expected [] to be undefined"},
		{File: "server/errors.test.ts", Line: 96, Code: "test-failure", Message: "[input error] Error: Snapshot mismatched"},
	}
	fresh, old := splitByBaseline(errs, base, "")
	if len(fresh) != 1 || fresh[0].Line != 96 || len(old) != 1 || old[0].Line != 58 {
		t.Fatalf("fresh=%+v old=%+v", fresh, old)
	}
}
