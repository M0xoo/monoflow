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

	// Test Overlay
	g := graph.New(tmpDir)
	g.AddPackage(pkg)

	overlayDir, err := AssembleFullAppOverlay(tmpDir, g, []string{"services/billing"})
	if err != nil {
		t.Fatalf("AssembleFullAppOverlay failed: %v", err)
	}

	if _, err := os.Stat(overlayDir); os.IsNotExist(err) {
		t.Errorf("overlayDir does not exist: %s", overlayDir)
	}
}
