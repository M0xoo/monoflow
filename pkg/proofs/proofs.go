// Package proofs lets remote CI reuse verification results an agent already produced for
// the exact same inputs. After a push, the passing records of the pushed commit are stored
// on the remote under refs/monoflow/proofs/<sha> (a plain git ref, no extra service); CI
// imports them into the global cache before `verify --affected`.
//
// Trust model: a record only matches when content, dependency sources, command and platform
// (OS/arch) are identical, and the generated workflow imports proofs for pull_request runs
// only. Pushes to the default branch always verify for real, so a wrong or forged proof can
// at worst make a PR look green, never leave the default branch unverified.
package proofs

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/cache"
)

// Ref is the remote ref holding the proofs of a commit.
func Ref(sha string) string { return "refs/monoflow/proofs/" + sha }

// Bundle is what gets published for a commit.
type Bundle struct {
	Commit    string                     `json:"commit"`
	Platform  string                     `json:"platform"`
	CreatedAt time.Time                  `json:"created_at"`
	Records   []cache.VerificationRecord `json:"records"`
}

func git(root string, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Publish pushes the passing records for keys as the proofs of sha.
func Publish(root, remote, sha string, mgr *cache.Manager, keys []string) (int, error) {
	b := Bundle{Commit: sha, Platform: runtime.GOOS + "/" + runtime.GOARCH, CreatedAt: time.Now().UTC()}
	for _, k := range keys {
		if rec, ok := mgr.PeekVerification(k); ok && rec.Status == "passed" {
			rec.Output = "" // results only: logs stay local
			b.Records = append(b.Records, *rec)
		}
	}
	if len(b.Records) == 0 {
		return 0, nil
	}
	data, _ := json.MarshalIndent(b, "", "  ")
	blob, err := git(root, string(data), "hash-object", "-w", "--stdin")
	if err != nil {
		return 0, err
	}
	tree, err := git(root, "100644 blob "+blob+"\tproofs.json\n", "mktree")
	if err != nil {
		return 0, err
	}
	commit, err := git(root, "", "commit-tree", tree, "-m", "monoflow proofs for "+sha)
	if err != nil {
		return 0, err
	}
	if _, err := git(root, "", "push", "--quiet", "--force", remote, commit+":"+Ref(sha)); err != nil {
		return 0, err
	}
	return len(b.Records), nil
}

// Import fetches the proofs of sha and stores the records matching this platform in the
// cache. It returns how many records were imported.
func Import(root, remote, sha string, mgr *cache.Manager) (int, error) {
	if _, err := git(root, "", "fetch", "--quiet", remote, "+"+Ref(sha)+":"+Ref(sha)); err != nil {
		return 0, nil // no proofs published for this commit
	}
	data, err := git(root, "", "show", Ref(sha)+":proofs.json")
	if err != nil {
		return 0, err
	}
	var b Bundle
	if err := json.Unmarshal([]byte(data), &b); err != nil {
		return 0, err
	}
	if b.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		return 0, nil // results from another platform are never reused
	}
	n := 0
	for _, rec := range b.Records {
		r := rec
		r.Output = fmt.Sprintf("monoflow-proof: agent result for %s (%s)", b.Commit[:min(8, len(b.Commit))], b.CreatedAt.Format(time.RFC3339))
		if r.Status == "passed" && mgr.PutVerification(&r) == nil {
			n++
		}
	}
	return n, nil
}

// Trusted reports whether the repo allows CI to reuse agent proofs (default true; set
// "ci": {"trust_local_proofs": false} in .agents/monoflow.config.json to disable).
func Trusted(root string) bool {
	data, err := os.ReadFile(root + "/.agents/monoflow.config.json")
	if err != nil {
		return true
	}
	var cfg struct {
		CI *struct {
			TrustLocalProofs *bool `json:"trust_local_proofs"`
		} `json:"ci"`
	}
	if json.Unmarshal(data, &cfg) == nil && cfg.CI != nil && cfg.CI.TrustLocalProofs != nil {
		return *cfg.CI.TrustLocalProofs
	}
	return true
}
