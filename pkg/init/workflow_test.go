package initialize

import (
	"strings"
	"testing"

	"github.com/antigravity/monoflow/pkg/graph"
)

// The generated CI must stay economical (one run per PR head, no drafts/docs) and must be
// able to install MonoFlow in repos without Go.
func TestGitHubWorkflowContract(t *testing.T) {
	g := graph.New(t.TempDir())
	g.AddPackage(&graph.PackageNode{Name: "@a/ui", Path: "packages/ui", Language: graph.LangTypeScript})
	wf := GitHubWorkflow(g)
	for _, want := range []string{
		"pull_request:", "branches: [main, master]", "cancel-in-progress: true",
		"github.event.pull_request.draft == false", `"**/*.md"`, "actions/setup-node",
		"actions/setup-go", "monoflow verify --affected --base=",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow missing %q", want)
		}
	}
	if strings.Contains(wf, "push:\n    branches: [\"**\"]") {
		t.Errorf("branch pushes must not trigger CI")
	}
}
