package cache

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestCacheServerEndpoints(t *testing.T) {
	temp := t.TempDir()
	storage := filepath.Join(temp, "remote_cache_root")
	server := NewServer(storage, "secret_bearer_999")

	// 1. Health check (no auth needed)
	reqHealth := httptest.NewRequest("GET", "/health", nil)
	recHealth := httptest.NewRecorder()
	server.ServeHTTP(recHealth, reqHealth)
	if recHealth.Code != http.StatusOK {
		t.Fatalf("expected 200 on /health, got %d", recHealth.Code)
	}

	// 2. Unauthorized request to artifact
	reqNoAuth := httptest.NewRequest("GET", "/v8/artifacts/key123", nil)
	recNoAuth := httptest.NewRecorder()
	server.ServeHTTP(recNoAuth, reqNoAuth)
	if recNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", recNoAuth.Code)
	}

	// 3. Authorized PUT artifact
	payload := []byte("binary_artifact_bytes_example")
	reqPut := httptest.NewRequest("PUT", "/v8/artifacts/key123?type=artifact-tar", bytes.NewReader(payload))
	reqPut.Header.Set("Authorization", "Bearer secret_bearer_999")
	recPut := httptest.NewRecorder()
	server.ServeHTTP(recPut, reqPut)
	if recPut.Code != http.StatusOK {
		t.Fatalf("expected 200 on PUT, got %d: %s", recPut.Code, recPut.Body.String())
	}

	// 4. Authorized GET artifact (HIT)
	reqGet := httptest.NewRequest("GET", "/v8/artifacts/key123?type=artifact-tar", nil)
	reqGet.Header.Set("Authorization", "Bearer secret_bearer_999")
	recGet := httptest.NewRecorder()
	server.ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET, got %d", recGet.Code)
	}
	if recGet.Body.String() != string(payload) {
		t.Fatalf("unexpected body: got %s, want %s", recGet.Body.String(), string(payload))
	}

	// 5. GET non-existent artifact (MISS)
	reqMiss := httptest.NewRequest("GET", "/v8/artifacts/unknown_key?type=artifact-tar", nil)
	reqMiss.Header.Set("Authorization", "Bearer secret_bearer_999")
	recMiss := httptest.NewRecorder()
	server.ServeHTTP(recMiss, reqMiss)
	if recMiss.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on MISS, got %d", recMiss.Code)
	}

	// 6. Path traversal rejection
	reqTraverse := httptest.NewRequest("GET", "/v8/artifacts/../etc/passwd", nil)
	reqTraverse.Header.Set("Authorization", "Bearer secret_bearer_999")
	recTraverse := httptest.NewRecorder()
	server.ServeHTTP(recTraverse, reqTraverse)
	if recTraverse.Code != http.StatusBadRequest && recTraverse.Code != http.StatusNotFound {
		t.Fatalf("expected 400 or 404 on traversal attempt, got %d", recTraverse.Code)
	}
}
