package telemetry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMetricsCollector(t *testing.T) {
	c := NewCollector()

	dur := c.Measure("test_phase", func() {
		time.Sleep(5 * time.Millisecond)
	})

	if dur < 4.0 {
		t.Fatalf("expected duration >= 4ms, got %f", dur)
	}

	c.Record("manual_phase", 10*time.Millisecond, "custom_meta")

	summary := c.Summary()
	if _, ok := summary["test_phase"]; !ok {
		t.Errorf("missing test_phase in summary")
	}
	if _, ok := summary["manual_phase"]; !ok {
		t.Errorf("missing manual_phase in summary")
	}
	if _, ok := summary["total_ms"]; !ok {
		t.Errorf("missing total_ms in summary")
	}
}

func TestSaveTrace(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "monoflow_trace_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	trace := ExecutionTrace{
		TaskID:    "task-123",
		StartedAt: time.Now().Format(time.RFC3339),
		TotalMs:   45.2,
		Metrics:   map[string]float64{"inner_loop_ms": 32.1},
	}

	err = SaveTrace(tmpDir, "task-123", trace)
	if err != nil {
		t.Fatalf("failed to save trace: %v", err)
	}

	filePath := filepath.Join(tmpDir, ".agents", "traces", "task-123.json")
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatalf("trace file not found at %s", filePath)
	}
}
