package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// QuarantinedRecord stores metadata for a quarantined flaky test.
type QuarantinedRecord struct {
	TestID         string    `json:"test_id"`
	QuarantinedAt  time.Time `json:"quarantined_at"`
	Reason         string    `json:"reason"`
	FailureCount   int       `json:"failure_count"`
	LastFailureLog string    `json:"last_failure_log,omitempty"`
}

// QuarantineManager isolates flaky tests from the agent's feedback loop.
type QuarantineManager struct {
	mu       sync.RWMutex
	filePath string
	records  map[string]QuarantinedRecord
}

// NewQuarantineManager loads or initializes the quarantine registry.
func NewQuarantineManager(rootDir string) *QuarantineManager {
	qm := &QuarantineManager{
		filePath: filepath.Join(rootDir, ".agents", "quarantine.json"),
		records:  make(map[string]QuarantinedRecord),
	}
	_ = qm.load()
	return qm
}

func (qm *QuarantineManager) load() error {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	if _, err := os.Stat(qm.filePath); os.IsNotExist(err) {
		return nil
	}

	data, err := os.ReadFile(qm.filePath)
	if err != nil {
		return err
	}

	var list []QuarantinedRecord
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}

	qm.records = make(map[string]QuarantinedRecord)
	for _, r := range list {
		qm.records[r.TestID] = r
	}
	return nil
}

func (qm *QuarantineManager) save() error {
	dir := filepath.Dir(qm.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	list := make([]QuarantinedRecord, 0, len(qm.records))
	for _, r := range qm.records {
		list = append(list, r)
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(qm.filePath, data, 0644)
}

// IsQuarantined checks if a test or error message belongs to a quarantined item.
func (qm *QuarantineManager) IsQuarantined(identifier string) bool {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	for k := range qm.records {
		if strings.Contains(identifier, k) || strings.Contains(k, identifier) {
			return true
		}
	}
	return false
}

// Quarantine adds a test to the quarantine registry.
func (qm *QuarantineManager) Quarantine(testID, reason, lastLog string) error {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	rec, exists := qm.records[testID]
	if !exists {
		rec = QuarantinedRecord{
			TestID:        testID,
			QuarantinedAt: time.Now().UTC(),
			Reason:        reason,
			FailureCount:  1,
		}
	} else {
		rec.FailureCount++
	}
	rec.LastFailureLog = lastLog
	qm.records[testID] = rec

	return qm.save()
}

// List returns all quarantined records.
func (qm *QuarantineManager) List() []QuarantinedRecord {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	res := make([]QuarantinedRecord, 0, len(qm.records))
	for _, r := range qm.records {
		res = append(res, r)
	}
	return res
}
