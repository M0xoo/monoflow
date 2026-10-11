package verify

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/antigravity/monoflow/pkg/graph"
)

// fix_cmd runs on the package's changed files of the kinds it names, reports the files it
// changed, reports a failing command without touching anything else, and "none" is off.
func TestFixChanged(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0755)
		_ = os.WriteFile(filepath.Join(root, rel), []byte(s), 0644)
	}
	write("a/x.txt", "too   many  spaces\n")
	write("a/y.txt", "fine\n")
	write("a/z.md", "md   spaces\n")
	write("b/x.txt", "b   spaces\n")
	write("c/x.txt", "c   spaces\n")
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "a", Path: "a", FixCmd: `perl -pi -e 's/ +/ /g' {files:.txt}`})
	g.AddPackage(&graph.PackageNode{Name: "b", Path: "b", FixCmd: `echo "prettier: not found"; exit 127`})
	g.AddPackage(&graph.PackageNode{Name: "c", Path: "c", FixCmd: "none"})

	res := FixChanged(root, g, []string{"a/x.txt", "a/y.txt", "a/z.md", "b/x.txt", "c/x.txt", "a/deleted.txt"})
	if len(res.Fixed) != 1 || res.Fixed[0] != "a/x.txt" {
		t.Fatalf("fixed: %v", res.Fixed)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a/z.md")); string(got) != "md   spaces\n" {
		t.Fatalf("{files:.txt} must not touch other kinds: %q", got)
	}
	if len(res.Failed) != 1 || res.Failed[0] != "b: prettier: not found" {
		t.Fatalf("failed: %v", res.Failed)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "c/x.txt")); string(got) != "c   spaces\n" {
		t.Fatalf(`"none" must not run: %q`, got)
	}
}
