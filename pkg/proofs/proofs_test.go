package proofs_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antigravity/monoflow/pkg/cache"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/proofs"
	"github.com/antigravity/monoflow/pkg/verify"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func runner(root, cacheDir string) (*verify.Runner, *cache.Manager, *graph.PackageNode) {
	g := graph.New(root)
	pkg := &graph.PackageNode{Name: "lib", Path: "lib", TestCmd: "test -f lib.txt"}
	g.AddPackage(pkg)
	g.RebuildEdges()
	mgr := cache.NewManager(root, cache.Config{Enabled: true, Dir: cacheDir})
	r := verify.NewRunner(root)
	r.SetCacheManager(mgr)
	r.SetGraph(g)
	return r, mgr, pkg
}

// CI reuses an agent's result only for identical inputs on the same platform.
func TestPublishImportRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	git(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	agent := filepath.Join(tmp, "agent")
	git(t, tmp, "clone", "-q", remote, agent)
	git(t, agent, "config", "user.email", "t@t")
	git(t, agent, "config", "user.name", "t")
	_ = os.MkdirAll(filepath.Join(agent, "lib"), 0755)
	_ = os.WriteFile(filepath.Join(agent, "lib", "lib.txt"), []byte("v1\n"), 0644)
	git(t, agent, "add", "-A")
	git(t, agent, "commit", "-qm", "lib")
	git(t, agent, "push", "-q", "origin", "HEAD:main")
	sha := git(t, agent, "rev-parse", "HEAD")

	r, mgr, pkg := runner(agent, filepath.Join(tmp, "agent-cache"))
	res := r.VerifyTarget(pkg, verify.VerifyOptions{Strict: true})
	if res.Status != "passed" || res.CacheKey == "" {
		t.Fatalf("agent verify: %+v", res)
	}
	if n, err := proofs.Publish(agent, "origin", sha, mgr, verify.PassedKeys([]verify.TargetResult{res})); err != nil || n != 1 {
		t.Fatalf("publish: n=%d err=%v", n, err)
	}

	ci := filepath.Join(tmp, "ci")
	git(t, tmp, "clone", "-q", remote, ci)
	cr, cmgr, cpkg := runner(ci, filepath.Join(tmp, "ci-cache"))
	if n, err := proofs.Import(ci, "origin", sha, cmgr); err != nil || n != 1 {
		t.Fatalf("import: n=%d err=%v", n, err)
	}
	got := cr.VerifyTarget(cpkg, verify.VerifyOptions{Strict: true})
	if !got.Cached || !strings.HasPrefix(got.CachedFrom, "monoflow-proof:") {
		t.Fatalf("CI should reuse the agent proof, got %+v", got)
	}

	// A proof from another platform is ignored.
	data := git(t, ci, "show", proofs.Ref(sha)+":proofs.json")
	var b proofs.Bundle
	_ = json.Unmarshal([]byte(data), &b)
	b.Platform = "plan9/mips"
	raw, _ := json.Marshal(b)
	blob := strings.TrimSpace(func() string {
		cmd := exec.Command("git", "hash-object", "-w", "--stdin")
		cmd.Dir = agent
		cmd.Stdin = strings.NewReader(string(raw))
		out, _ := cmd.Output()
		return string(out)
	}())
	treeCmd := exec.Command("git", "mktree")
	treeCmd.Dir = agent
	treeCmd.Stdin = strings.NewReader("100644 blob " + blob + "\tproofs.json\n")
	tree, _ := treeCmd.Output()
	commit := git(t, agent, "commit-tree", strings.TrimSpace(string(tree)), "-m", "other platform")
	git(t, agent, "push", "-q", "--force", "origin", commit+":"+proofs.Ref(sha))
	other := filepath.Join(tmp, "ci2")
	git(t, tmp, "clone", "-q", remote, other)
	_, omgr, _ := runner(other, filepath.Join(tmp, "ci2-cache"))
	if n, _ := proofs.Import(other, "origin", sha, omgr); n != 0 {
		t.Fatalf("proofs from another platform must not be imported, got %d", n)
	}
}
