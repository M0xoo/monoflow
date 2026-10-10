package cache

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Server implements a Turborepo/Nx-compatible HTTP Remote Cache server.
type Server struct {
	mu         sync.RWMutex
	StorageDir string
	AuthToken  string
}

// NewServer creates a new remote cache HTTP handler.
func NewServer(storageDir string, authToken string) *Server {
	_ = os.MkdirAll(filepath.Join(storageDir, "verify"), 0755)
	_ = os.MkdirAll(filepath.Join(storageDir, "artifact-meta"), 0755)
	_ = os.MkdirAll(filepath.Join(storageDir, "artifact-tar"), 0755)

	return &Server{
		StorageDir: storageDir,
		AuthToken:  authToken,
	}
}

// ServeHTTP handles GET, PUT, and health checks for remote cache requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Health check endpoint
	if r.URL.Path == "/health" || r.URL.Path == "/" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status": "healthy", "service": "monoflow-remote-cache", "storage_dir": %q}`+"\n", s.StorageDir)
		return
	}

	// 2. Authentication check
	if s.AuthToken != "" {
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + s.AuthToken
		if auth != expected && r.Header.Get("X-Cache-Token") != s.AuthToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error": "unauthorized", "message": "invalid or missing bearer token"}`+"\n")
			return
		}
	}

	// 3. Artifact routing: /v8/artifacts/:key
	if !strings.HasPrefix(r.URL.Path, "/v8/artifacts/") {
		http.NotFound(w, r)
		return
	}

	key := strings.TrimPrefix(r.URL.Path, "/v8/artifacts/")
	if key == "" || strings.Contains(key, "..") || strings.Contains(key, "/") {
		http.Error(w, `{"error": "invalid artifact key"}`, http.StatusBadRequest)
		return
	}

	category := r.URL.Query().Get("type")
	if category == "" {
		category = "artifact-tar"
	}
	if category != "verify" && category != "artifact-meta" && category != "artifact-tar" {
		category = "artifact-tar"
	}

	filePath := filepath.Join(s.StorageDir, category, key)

	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		data, err := os.ReadFile(filePath)
		s.mu.RUnlock()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"status": "miss", "key": %q}`+"\n", key)
			return
		}

		if category == "artifact-tar" {
			w.Header().Set("Content-Type", "application/gzip")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "failed to read body: %v"}`, err), http.StatusInternalServerError)
			return
		}

		s.mu.Lock()
		_ = os.MkdirAll(filepath.Dir(filePath), 0755)
		err = os.WriteFile(filePath, body, 0644)
		s.mu.Unlock()

		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "failed to save artifact: %v"}`, err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status": "uploaded", "key": %q}`+"\n", key)

	default:
		http.Error(w, `{"error": "method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// StartServer launches the HTTP remote cache daemon.
func StartServer(addr string, storageDir string, authToken string) error {
	srv := NewServer(storageDir, authToken)
	mux := http.NewServeMux()
	mux.Handle("/", srv)

	return http.ListenAndServe(addr, mux)
}
