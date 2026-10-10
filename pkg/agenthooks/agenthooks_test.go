package agenthooks

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	blocked := []string{
		"git commit -m wip",
		"cd services/ledger && go test ./...",
		"go vet ./... ; git push origin feat",
		"gh pr create --title x",
		"npm test",
		"pnpm run build",
		"git checkout feature-x",
		"CGO_ENABLED=0 go build ./...",
		"npx tsc --noEmit",
	}
	for _, c := range blocked {
		if Check(c) == nil {
			t.Errorf("expected %q to be redirected", c)
		}
	}
	allowed := []string{
		"git status --short",
		"git diff HEAD",
		"git log --oneline -5",
		"go test ./pkg/x -run TestFoo",
		"git checkout -- file.go",
		"./bin/monoflow verify --affected",
		"MONOFLOW_ALLOW_RAW=1 go test ./...",
		"cat go.mod",
		"echo 'go test is great'",
	}
	for _, c := range allowed {
		if r := Check(c); r != nil {
			t.Errorf("expected %q to be allowed, got %s", c, r.Replacement)
		}
	}
}

func TestClaudePreToolUse(t *testing.T) {
	state := t.TempDir()
	out := ClaudePreToolUse(strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`), state)
	if !strings.Contains(string(out), `"permissionDecision":"deny"`) || !strings.Contains(string(out), "monoflow verify") {
		t.Fatalf("unexpected hook output: %s", out)
	}
	// Soft rule: an identical retry is allowed (the agent really needs the raw command).
	if out := ClaudePreToolUse(strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`), state); out != nil {
		t.Fatalf("retry of a soft-nudged command must be allowed, got %s", out)
	}
	// Hard rule: never allowed by retrying.
	for i := 0; i < 2; i++ {
		if out := ClaudePreToolUse(strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"git push origin x"}}`), state); out == nil {
			t.Fatalf("git push must stay blocked on attempt %d", i+1)
		}
	}
	if out := ClaudePreToolUse(strings.NewReader(`{"tool_name":"Read","tool_input":{}}`), state); out != nil {
		t.Fatalf("non-Bash tools must pass through, got %s", out)
	}
}
