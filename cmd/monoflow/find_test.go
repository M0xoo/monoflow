package main

import (
	"strings"
	"testing"
)

func TestFindGroupsAndLeavesOutGenerated(t *testing.T) {
	out := findText("BAD", strings.Join([]string{
		"src/codes.ts:20:  BAD: -32600,",
		"src/codes.test.ts:5:expect(BAD)",
		"src/codes.test.ts:9:expect(BAD)",
		"docs/errors.md:3:BAD means",
		"CHANGELOG.md:100:BAD renamed",
		"docs/reference/api.md:7:BAD",
	}, "\n"))
	for _, want := range []string{"BAD: 6 matches; files: code 1, tests 1, docs 1 (2 in changelogs/generated/build output left out)", "  src/codes.ts:20  BAD: -32600,", "  src/codes.test.ts:5,9", "  docs/errors.md:3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "CHANGELOG") {
		t.Fatalf("changelog must be left out:\n%s", out)
	}
}
