package pr

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Cloud sandboxes (e.g. Claude Code on the web) run on a detached or environment-managed
// checkout with no authenticated gh. pr create must neither switch the checkout nor fail
// silently: it reports why no hosted PR was opened.
func TestCreateNeverSwitchesCheckoutAndExplainsRemote(t *testing.T) {
	repo := setupTestGitRepo(t)
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("checkout", "-q", "--detach")
	_ = os.WriteFile(filepath.Join(repo, "packages", "core", "index.js"), []byte("module.exports = { core: 2 };\n"), 0644)
	git("add", "-A")
	git("commit", "-qm", "change core")
	head := git("rev-parse", "HEAD")

	res, err := NewManager(repo).Create(CreateOptions{Title: "feat(core): v2", Body: "core v2", BaseBranch: "main", AutoPush: true, Draft: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if cur := git("rev-parse", "--abbrev-ref", "HEAD"); cur != "HEAD" {
		t.Fatalf("pr create switched the checkout to %q", cur)
	}
	if got := git("rev-parse", "refs/heads/"+res.HeadBranch); got != head {
		t.Fatalf("head branch %s should point at HEAD %s, got %s", res.HeadBranch, head, got)
	}
	if res.RemotePR == nil || res.RemotePR.Status == "created" || res.RemotePR.Reason == "" || res.RemotePR.Directive == "" {
		t.Fatalf("expected an explained not_created remote_pr (no remote), got %+v", res.RemotePR)
	}
}

func TestExtractGitHubRepoCloudProxy(t *testing.T) {
	owner, repo, ok := extractGitHubRepo("http://local_proxy@127.0.0.1:41541/git/M0xoo/monoflow")
	if !ok || owner != "M0xoo" || repo != "monoflow" {
		t.Fatalf("got %s/%s ok=%v", owner, repo, ok)
	}
}
