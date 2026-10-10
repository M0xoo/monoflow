package deliver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/antigravity/monoflow/pkg/graph"
)

func TestBuildDeliverableAndOverlay(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "monoflow_deliver_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	pkgPath := filepath.Join(tmpDir, "services", "billing")
	_ = os.MkdirAll(pkgPath, 0755)
	_ = os.WriteFile(filepath.Join(pkgPath, "index.ts"), []byte("export const a = 1;"), 0644)

	pkg := &graph.PackageNode{
		Name:     "services/billing",
		Path:     "services/billing",
		Language: graph.LangTypeScript,
	}

	manifest, err := BuildDeliverable(tmpDir, pkg)
	if err != nil {
		t.Fatalf("BuildDeliverable failed: %v", err)
	}

	if manifest.Target != "services/billing" {
		t.Errorf("unexpected target: %s", manifest.Target)
	}
	if manifest.SHA256 == "" {
		t.Errorf("empty sha256 in manifest")
	}

	// 2. Second build of identical package should HIT cache and produce identical SHA256
	manifest2, err := BuildDeliverable(tmpDir, pkg)
	if err != nil {
		t.Fatalf("second BuildDeliverable failed: %v", err)
	}
	if !manifest2.Cached {
		t.Errorf("expected second build to hit cache (cached=true)")
	}
	if manifest.SHA256 != manifest2.SHA256 {
		t.Errorf("expected deterministic sha256, got %s vs %s", manifest.SHA256, manifest2.SHA256)
	}

	// 3. Test Overlay
	g := graph.New(tmpDir)
	g.AddPackage(pkg)

	overlayDir, err := AssembleFullAppOverlay(tmpDir, g, []string{"services/billing"})
	if err != nil {
		t.Fatalf("AssembleFullAppOverlay failed: %v", err)
	}

	if _, err := os.Stat(overlayDir); os.IsNotExist(err) {
		t.Errorf("overlayDir does not exist: %s", overlayDir)
	}

	// Check mounted package in overlay
	mountedPkg := filepath.Join(overlayDir, "services", "billing")
	if _, err := os.Stat(mountedPkg); os.IsNotExist(err) {
		t.Errorf("expected mounted package at %s", mountedPkg)
	}
}
