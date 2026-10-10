package scenario

import (
	"testing"
)

func TestFullMultiAgentScenario(t *testing.T) {
	report, err := RunScenario("")
	if err != nil {
		t.Fatalf("RunScenario failed: %v", err)
	}

	if report.Status != "success" {
		t.Fatalf("expected report status success, got %s", report.Status)
	}

	if report.TotalScenes != 7 {
		t.Errorf("expected 7 scenes, got %d", report.TotalScenes)
	}

	if report.Passed != 7 {
		t.Errorf("expected 7 passed scenes, got %d", report.Passed)
	}

	for _, sc := range report.Scenes {
		if sc.Status != "PASSED" && sc.Status != "BLOCKED_AS_EXPECTED" {
			t.Errorf("scene %d (%s) failed with status %s", sc.SceneNumber, sc.Title, sc.Status)
		}
	}
}
