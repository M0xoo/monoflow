package worktree

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

// Concurrent agents (separate processes, modelled by separate Managers) must not lose
// each other's registry entries (round-3 simulation: `worktree list` returned []).
func TestConcurrentCreatesKeepAllRegistryEntries(t *testing.T) {
	repo := setupTestRepo(t)
	defer os.RemoveAll(repo)

	const n = 6
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := NewManager(repo).Create(fmt.Sprintf("agent-%d", i)); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Logf("create error (git contention is acceptable, lost entries are not): %v", err)
	}
	list, err := NewManager(repo).List()
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for i := 0; i < n; i++ {
		if _, err := os.Stat(fmt.Sprintf("%s/.agents/worktrees/agent-%d", repo, i)); err == nil {
			created++
		}
	}
	if len(list) != created {
		t.Fatalf("registry has %d entries but %d worktrees exist on disk", len(list), created)
	}
	if created < n-1 {
		t.Fatalf("expected concurrent creates to succeed, only %d/%d did", created, n)
	}
}
