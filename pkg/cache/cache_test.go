package cache

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antigravity/monoflow/pkg/graph"
)

func TestDeterministicPackageKey(t *testing.T) {
	temp := t.TempDir()
	pkgDir := filepath.Join(temp, "services", "billing")
	_ = os.MkdirAll(pkgDir, 0755)
	_ = os.WriteFile(filepath.Join(pkgDir, "tax.ts"), []byte("export const calc = (a: number) => a * 0.2;"), 0644)

	pkg := &graph.PackageNode{
		Name:     "billing",
		Path:     "services/billing",
		Language: graph.LangTypeScript,
	}

	mgr := NewManager(temp, Config{Enabled: true})

	key1, err := mgr.ComputePackageKey(pkg, nil, "npm test", "test")
	if err != nil {
		t.Fatalf("ComputePackageKey failed: %v", err)
	}

	key2, err := mgr.ComputePackageKey(pkg, nil, "npm test", "test")
	if err != nil {
		t.Fatalf("ComputePackageKey failed: %v", err)
	}

	if key1 != key2 {
		t.Fatalf("expected identical keys for unchanged package, got %s vs %s", key1, key2)
	}

	// Modify file -> key MUST change
	_ = os.WriteFile(filepath.Join(pkgDir, "tax.ts"), []byte("export const calc = (a: number) => a * 0.25;"), 0644)
	key3, _ := mgr.ComputePackageKey(pkg, nil, "npm test", "test")
	if key1 == key3 {
		t.Fatalf("expected key to change when file is modified, but remained %s", key1)
	}

	// Changing upstream dependency -> key MUST change
	upstream := map[string]string{"contracts": "upstream_hash_abc"}
	key4, _ := mgr.ComputePackageKey(pkg, upstream, "npm test", "test")
	if key3 == key4 {
		t.Fatalf("expected key to change when upstream dependency hash changes")
	}
}

func TestVerificationCacheLocal(t *testing.T) {
	temp := t.TempDir()
	mgr := NewManager(temp, Config{Enabled: true})

	key := "test_verify_key_12345"
	rec := &VerificationRecord{
		Key:        key,
		Target:     "billing",
		Language:   "typescript",
		Status:     "passed",
		DurationMs: 125.4,
		CommandRun: "npm test",
		Timestamp:  time.Now().UTC(),
		Output:     "All 12 tests passed",
	}

	// 1. Initial cache miss
	if _, hit := mgr.GetVerification(key); hit {
		t.Fatal("expected cache miss before put")
	}

	// 2. Put verification
	if err := mgr.PutVerification(rec); err != nil {
		t.Fatalf("PutVerification failed: %v", err)
	}

	// 3. Cache hit
	cached, hit := mgr.GetVerification(key)
	if !hit {
		t.Fatal("expected cache hit after put")
	}
	if cached.Target != "billing" || cached.Status != "passed" {
		t.Fatalf("unexpected cached verification: %+v", cached)
	}

	stats := mgr.GetStats()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestArtifactPackagingAndCache(t *testing.T) {
	temp := t.TempDir()
	mgr := NewManager(temp, Config{Enabled: true})

	sourceDir := filepath.Join(temp, "dist_source")
	_ = os.MkdirAll(sourceDir, 0755)
	_ = os.WriteFile(filepath.Join(sourceDir, "bundle.js"), []byte("console.log('built');"), 0644)
	_ = os.WriteFile(filepath.Join(sourceDir, "index.d.ts"), []byte("export const x: number;"), 0644)

	key := "artifact_billing_hash_987"
	artRec := &ArtifactRecord{
		Key:             key,
		Target:          "billing",
		Language:        "typescript",
		ArtifactSHA:     "dummy_sha_abc",
		BuildDurationMs: 45.2,
		BuiltAt:         time.Now().UTC(),
	}

	// Put artifact
	if err := mgr.PutArtifact(key, sourceDir, artRec); err != nil {
		t.Fatalf("PutArtifact failed: %v", err)
	}

	// Extract artifact into a new target directory
	targetDir := filepath.Join(temp, "dist_target")
	restored, hit := mgr.GetArtifact(key, targetDir)
	if !hit {
		t.Fatal("expected artifact cache hit")
	}
	if restored.Target != "billing" {
		t.Fatalf("unexpected target in restored artifact: %s", restored.Target)
	}

	// Verify extracted files exist and match content
	bundleData, err := os.ReadFile(filepath.Join(targetDir, "bundle.js"))
	if err != nil || string(bundleData) != "console.log('built');" {
		t.Fatalf("extracted bundle.js content mismatch or missing: %v", err)
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRemoteHTTPCache(t *testing.T) {
	var mu sync.Mutex
	remoteStore := make(map[string][]byte)

	// Mock Remote Cache Handler (Turborepo/Nx REST API compatible)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer secret_token_123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		key := strings.TrimPrefix(r.URL.Path, "/v8/artifacts/")
		cat := r.URL.Query().Get("type")
		storageKey := cat + ":" + key

		mu.Lock()
		defer mu.Unlock()

		if r.Method == "GET" {
			data, exists := remoteStore[storageKey]
			if !exists {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}

		if r.Method == "PUT" {
			body, _ := io.ReadAll(r.Body)
			remoteStore[storageKey] = body
			w.WriteHeader(http.StatusOK)
			return
		}

		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	// Seed remote cache with a verification record
	rec := VerificationRecord{
		Key:        "remote_key_555",
		Target:     "auth-service",
		Language:   "go",
		Status:     "passed",
		DurationMs: 12.0,
		CommandRun: "go test",
		Timestamp:  time.Now().UTC(),
	}
	data, _ := json.Marshal(rec)
	remoteStore["verify:remote_key_555"] = data

	// Create manager configured with mock HTTP remote cache
	temp := t.TempDir()
	mgr := NewManager(temp, Config{
		Enabled:     true,
		RemoteURL:   "https://remote-cache.monoflow.dev",
		RemoteToken: "secret_token_123",
	})

	// Inject in-memory roundtripper to bypass TCP networking in sandbox
	mockClient := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			return rec.Result(), nil
		}),
	}
	mgr.SetHTTPClient(mockClient)

	// Get from remote cache (should hit remote and populate local)
	fetched, hit := mgr.GetVerification("remote_key_555")
	if !hit {
		t.Fatal("expected remote cache hit")
	}
	if fetched.Target != "auth-service" {
		t.Fatalf("unexpected fetched target: %s", fetched.Target)
	}

	// Verify it was written through to local cache
	localPath := filepath.Join(temp, ".agents", "cache", "verify", "remote_key_555.json")
	if _, err := os.Stat(localPath); os.IsNotExist(err) {
		t.Fatal("expected remote record to be written to local cache")
	}
}

func TestRemoteDirectoryCache(t *testing.T) {
	temp := t.TempDir()
	sharedDir := filepath.Join(temp, "shared_remote_cache")
	localDir := filepath.Join(temp, "local_repo")

	mgr := NewManager(localDir, Config{
		Enabled:   true,
		RemoteURL: "file://" + sharedDir,
	})

	key := "dir_cache_key_999"
	rec := &VerificationRecord{
		Key:        key,
		Target:     "payments",
		Language:   "rust",
		Status:     "passed",
		DurationMs: 88.0,
		CommandRun: "cargo test",
		Timestamp:  time.Now().UTC(),
	}

	// Put verification (should write to local AND shared directory remote cache)
	if err := mgr.PutVerification(rec); err != nil {
		t.Fatalf("PutVerification failed: %v", err)
	}

	// Verify shared remote file exists
	sharedPath := filepath.Join(sharedDir, "verify", key)
	if _, err := os.Stat(sharedPath); os.IsNotExist(err) {
		t.Fatalf("expected remote file at %s", sharedPath)
	}

	// New manager simulating a clean local worker
	worker2Dir := filepath.Join(temp, "worker2_repo")
	mgrWorker2 := NewManager(worker2Dir, Config{
		Enabled:   true,
		RemoteURL: "file://" + sharedDir,
	})

	// Worker 2 should hit remote directory cache
	fetched, hit := mgrWorker2.GetVerification(key)
	if !hit {
		t.Fatal("worker 2 expected remote cache hit from directory")
	}
	if fetched.Target != "payments" {
		t.Fatalf("unexpected target: %s", fetched.Target)
	}
}
