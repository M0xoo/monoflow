package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MetricRecord stores a named phase duration in nanoseconds/milliseconds.
type MetricRecord struct {
	Phase      string  `json:"phase"`
	DurationMs float64 `json:"duration_ms"`
	Timestamp  string  `json:"timestamp"`
	Metadata   string  `json:"metadata,omitempty"`
}

// Collector records and aggregates execution metrics.
type Collector struct {
	mu      sync.Mutex
	records []MetricRecord
	start   time.Time
}

// NewCollector creates an initialized metrics collector.
func NewCollector() *Collector {
	return &Collector{
		records: make([]MetricRecord, 0),
		start:   time.Now(),
	}
}

// Measure executes a function and records its execution time.
func (c *Collector) Measure(phase string, fn func()) float64 {
	start := time.Now()
	fn()
	durationMs := float64(time.Since(start).Microseconds()) / 1000.0

	c.mu.Lock()
	c.records = append(c.records, MetricRecord{
		Phase:      phase,
		DurationMs: durationMs,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
	})
	c.mu.Unlock()

	return durationMs
}

// Record manually appends a phase duration.
func (c *Collector) Record(phase string, duration time.Duration, meta string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, MetricRecord{
		Phase:      phase,
		DurationMs: float64(duration.Microseconds()) / 1000.0,
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		Metadata:   meta,
	})
}

// TotalDurationMs returns the total time elapsed since collector creation.
func (c *Collector) TotalDurationMs() float64 {
	return float64(time.Since(c.start).Microseconds()) / 1000.0
}

// Summary returns a key-value map of all recorded phase durations.
func (c *Collector) Summary() map[string]float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := make(map[string]float64)
	for _, r := range c.records {
		res[r.Phase] = r.DurationMs
	}
	res["total_ms"] = c.TotalDurationMs()
	return res
}

// ExecutionTrace stores a persistent audit log of an agent's session.
type ExecutionTrace struct {
	TaskID       string                 `json:"task_id"`
	StartedAt    string                 `json:"started_at"`
	EndedAt      string                 `json:"ended_at"`
	TotalMs      float64                `json:"total_ms"`
	Checkpoints  []string               `json:"checkpoints"`
	Verifications []map[string]any      `json:"verifications"`
	Metrics      map[string]float64     `json:"metrics"`
	DiffSummary  string                 `json:"diff_summary"`
}

// SaveTrace writes the execution trace to .agents/traces/<task-id>.json.
func SaveTrace(rootDir, taskID string, trace ExecutionTrace) error {
	dir := filepath.Join(rootDir, ".agents", "traces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create traces directory: %w", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("%s.json", taskID))
	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode trace: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}
