package cache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/antigravity/monoflow/pkg/graph"
)

// Config configures local and remote package caching.
type Config struct {
	Enabled         bool   `json:"enabled"`
	Dir             string `json:"dir,omitempty"`
	RemoteURL       string `json:"remote_url,omitempty"`
	RemoteToken     string `json:"remote_token,omitempty"`
	RemoteTimeoutMs int    `json:"remote_timeout_ms,omitempty"`
}

// VerificationRecord records a cached typecheck/lint/test execution.
type VerificationRecord struct {
	Key        string    `json:"key"`
	Target     string    `json:"target"`
	Language   string    `json:"language"`
	Status     string    `json:"status"` // "passed" or "failed"
	DurationMs float64   `json:"duration_ms"`
	CommandRun string    `json:"command_run"`
	Timestamp  time.Time `json:"timestamp"`
	Output     string    `json:"output,omitempty"`
}

// ArtifactRecord records a cached package build deliverable.
type ArtifactRecord struct {
	Key             string    `json:"key"`
	Target          string    `json:"target"`
	Language        string    `json:"language"`
	ArtifactSHA     string    `json:"artifact_sha"`
	BuildDurationMs float64   `json:"build_duration_ms"`
	BuiltAt         time.Time `json:"built_at"`
	Files           []string  `json:"files,omitempty"`
}

// Stats summarizes cache efficiency and storage.
type Stats struct {
	Enabled        bool    `json:"enabled"`
	Hits           int     `json:"hits"`
	Misses         int     `json:"misses"`
	HitRatio       float64 `json:"hit_ratio"`
	LocalDir       string  `json:"local_dir"`
	TotalArtifacts int     `json:"total_artifacts"`
	TotalVerifies  int     `json:"total_verifications"`
	TotalBytes     int64   `json:"total_bytes"`
	RemoteEnabled  bool    `json:"remote_enabled"`
	RemoteURL      string  `json:"remote_url,omitempty"`
}

// Manager orchestrates local and remote caching for monoflow packages.
type Manager struct {
	mu          sync.RWMutex
	RootDir     string
	Config      Config
	LocalDir    string
	verifyDir   string
	artifactDir string
	httpClient  *http.Client
	stats       Stats
}

// DefaultConfig loads cache settings from environment and config.
func DefaultConfig(rootDir string) Config {
	cfg := Config{
		Enabled:         true,
		RemoteTimeoutMs: 2500,
	}

	if envNoCache := os.Getenv("MONOFLOW_NO_CACHE"); envNoCache == "1" || strings.ToLower(envNoCache) == "true" {
		cfg.Enabled = false
	}

	if envDir := os.Getenv("MONOFLOW_CACHE_DIR"); envDir != "" {
		cfg.Dir = envDir
	} else {
		cfg.Dir = SharedCacheDir(rootDir)
	}

	if envRemote := os.Getenv("MONOFLOW_REMOTE_CACHE_URL"); envRemote != "" {
		cfg.RemoteURL = envRemote
	}

	if envToken := os.Getenv("MONOFLOW_REMOTE_CACHE_TOKEN"); envToken != "" {
		cfg.RemoteToken = envToken
	}

	// Try reading .agents/monoflow.config.json if cache section exists
	configPath := filepath.Join(rootDir, ".agents", "monoflow.config.json")
	if data, err := os.ReadFile(configPath); err == nil {
		var raw struct {
			Cache *Config `json:"cache"`
		}
		if json.Unmarshal(data, &raw) == nil && raw.Cache != nil {
			if !raw.Cache.Enabled && os.Getenv("MONOFLOW_CACHE") != "1" {
				cfg.Enabled = raw.Cache.Enabled
			}
			if cfg.RemoteURL == "" && raw.Cache.RemoteURL != "" {
				cfg.RemoteURL = raw.Cache.RemoteURL
			}
			if cfg.RemoteToken == "" && raw.Cache.RemoteToken != "" {
				cfg.RemoteToken = raw.Cache.RemoteToken
			}
			if raw.Cache.RemoteTimeoutMs > 0 {
				cfg.RemoteTimeoutMs = raw.Cache.RemoteTimeoutMs
			}
			if os.Getenv("MONOFLOW_CACHE_DIR") == "" && raw.Cache.Dir != "" {
				cfg.Dir = raw.Cache.Dir
			}
		}
	}

	return cfg
}

// SharedCacheDir returns the package cache directory for rootDir. Linked git worktrees
// (where .git is a file) resolve to the main repository's .agents/cache so every agent
// worktree shares one cache and gets hits for untouched dependencies.
func SharedCacheDir(rootDir string) string {
	local := filepath.Join(rootDir, ".agents", "cache")
	if fi, err := os.Lstat(filepath.Join(rootDir, ".git")); err != nil || fi.IsDir() {
		return local
	}
	cmd := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = rootDir
	out, err := cmd.Output()
	if err != nil {
		return local
	}
	commonDir := strings.TrimSpace(string(out))
	if filepath.Base(commonDir) != ".git" {
		return local // bare or non-standard layout: keep the worktree-local cache
	}
	return filepath.Join(filepath.Dir(commonDir), ".agents", "cache")
}

// NewManager initializes a cache manager.
func NewManager(rootDir string, cfg Config) *Manager {
	if cfg.Dir == "" {
		cfg.Dir = filepath.Join(rootDir, ".agents", "cache")
	}

	verifyDir := filepath.Join(cfg.Dir, "verify")
	artifactDir := filepath.Join(cfg.Dir, "artifacts")

	if cfg.Enabled {
		_ = os.MkdirAll(verifyDir, 0755)
		_ = os.MkdirAll(artifactDir, 0755)
	}

	timeout := time.Duration(cfg.RemoteTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 2500 * time.Millisecond
	}

	mgr := &Manager{
		RootDir:     rootDir,
		Config:      cfg,
		LocalDir:    cfg.Dir,
		verifyDir:   verifyDir,
		artifactDir: artifactDir,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		stats: Stats{
			Enabled:       cfg.Enabled,
			LocalDir:      cfg.Dir,
			RemoteEnabled: cfg.RemoteURL != "",
			RemoteURL:     cfg.RemoteURL,
		},
	}

	return mgr
}

// SetHTTPClient configures a custom HTTP client (e.g. for testing with mock transport).
func (m *Manager) SetHTTPClient(client *http.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.httpClient = client
}

// UpstreamKeys returns source hashes of pkg's transitive live (source) dependencies, for
// use as ComputePackageKey's upstreamKeys: a changed dependency must invalidate the
// dependent's cached verification and build artifact. Pinned dependencies need no entry:
// the pinned version is part of the consumer's own (hashed) manifest.
func (m *Manager) UpstreamKeys(g *graph.DependencyGraph, pkg *graph.PackageNode) map[string]string {
	keys := map[string]string{}
	visited := map[string]bool{pkg.Name: true}
	queue := g.GetUpstream(pkg.Name)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if visited[name] {
			continue
		}
		visited[name] = true
		if dep, ok := g.GetPackage(name); ok {
			keys[name], _ = m.ComputePackageKey(dep, nil, "", "source")
		}
		queue = append(queue, g.GetUpstream(name)...)
	}
	return keys
}

// ComputePackageKey calculates the deterministic cryptographic fingerprint of a package's inputs:
// 1. Recursive hash of all package source files (ignoring non-source/transient directories)
// 2. Package manifest / configuration (name, language, version)
// 3. Command being run (build/test/typecheck)
// 4. Upstream dependencies' input keys
// 5. Architecture & OS runtime metadata
func (m *Manager) ComputePackageKey(pkg *graph.PackageNode, upstreamKeys map[string]string, command string, mode string) (string, error) {
	hasher := sha256.New()

	// 1. Target metadata
	fmt.Fprintf(hasher, "target:%s\nlang:%s\ncmd:%s\nmode:%s\nos:%s\narch:%s\n",
		pkg.Name, pkg.Language, command, mode, runtime.GOOS, runtime.GOARCH)

	// 2. Upstream dependency hashes (topologically ordered)
	if len(upstreamKeys) > 0 {
		var depNames []string
		for dep := range upstreamKeys {
			depNames = append(depNames, dep)
		}
		sort.Strings(depNames)
		for _, dep := range depNames {
			fmt.Fprintf(hasher, "dep:%s=%s\n", dep, upstreamKeys[dep])
		}
	}

	// 3. Package source tree files
	absPkgPath := filepath.Join(m.RootDir, pkg.Path)
	if _, err := os.Stat(absPkgPath); err == nil {
		fileList, err := collectSourceFiles(absPkgPath)
		if err == nil {
			for _, relPath := range fileList {
				fullPath := filepath.Join(absPkgPath, relPath)
				fileHash, err := hashFile(fullPath)
				if err == nil {
					fmt.Fprintf(hasher, "file:%s=%s\n", relPath, fileHash)
				}
			}
		}
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// collectSourceFiles returns sorted relative paths of all relevant source files.
func collectSourceFiles(dir string) ([]string, error) {
	var files []string
	ignoreDirs := map[string]bool{
		".git":          true,
		"node_modules":  true,
		"target":        true,
		".agents":       true,
		"dist":          true,
		".dist":         true,
		".next":         true,
		".turbo":        true,
		".venv":         true,
		"venv":          true,
		"__pycache__":   true,
		".pytest_cache": true,
		".mypy_cache":   true,
		".idea":         true,
		".vscode":       true,
	}

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if ignoreDirs[base] {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip binaries, temporary files, logs, sockets
		name := info.Name()
		if strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".tmp") ||
			strings.HasSuffix(name, ".swp") || name == ".DS_Store" {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})

	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// GetVerification checks local cache and remote cache for a verified test/typecheck run.
func (m *Manager) GetVerification(key string) (*VerificationRecord, bool) {
	if !m.Config.Enabled {
		return nil, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Check local cache
	localPath := filepath.Join(m.verifyDir, key+".json")
	if data, err := os.ReadFile(localPath); err == nil {
		var rec VerificationRecord
		if json.Unmarshal(data, &rec) == nil {
			m.recordStat("hits")
			return &rec, true
		}
	}

	// 2. Check remote cache
	if m.Config.RemoteURL != "" {
		if data, ok := m.fetchRemote(key, "verify"); ok {
			var rec VerificationRecord
			if json.Unmarshal(data, &rec) == nil {
				// Write through to local cache
				_ = os.WriteFile(localPath, data, 0644)
				m.recordStat("hits")
				return &rec, true
			}
		}
	}

	m.recordStat("misses")
	return nil, false
}

// PutVerification stores a successful verification run locally and optionally syncs to remote.
func (m *Manager) PutVerification(rec *VerificationRecord) error {
	if !m.Config.Enabled || rec == nil || rec.Key == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}

	localPath := filepath.Join(m.verifyDir, rec.Key+".json")
	if err := os.WriteFile(localPath, data, 0644); err != nil {
		return err
	}

	// Push to remote cache
	if m.Config.RemoteURL != "" {
		if strings.HasPrefix(m.Config.RemoteURL, "file://") || filepath.IsAbs(m.Config.RemoteURL) {
			_ = m.pushRemote(rec.Key, "verify", data)
		} else {
			go func(k string, payload []byte) {
				_ = m.pushRemote(k, "verify", payload)
			}(rec.Key, data)
		}
	}

	return nil
}

// GetArtifact checks if a built deliverable artifact exists in local or remote cache.
// On hit, it uncompresses the archive directly into targetDir.
func (m *Manager) GetArtifact(key string, targetDir string) (*ArtifactRecord, bool) {
	if !m.Config.Enabled {
		return nil, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	archivePath := filepath.Join(m.artifactDir, key+".tar.gz")
	metaPath := filepath.Join(m.artifactDir, key+".meta.json")

	// 1. Check local cache
	if metaData, err := os.ReadFile(metaPath); err == nil {
		var rec ArtifactRecord
		if json.Unmarshal(metaData, &rec) == nil {
			if _, err := os.Stat(archivePath); err == nil {
				if err := extractTarGz(archivePath, targetDir); err == nil {
					m.recordStat("hits")
					return &rec, true
				}
			}
		}
	}

	// 2. Check remote cache
	if m.Config.RemoteURL != "" {
		if metaData, ok := m.fetchRemote(key, "artifact-meta"); ok {
			var rec ArtifactRecord
			if json.Unmarshal(metaData, &rec) == nil {
				if tarData, ok := m.fetchRemote(key, "artifact-tar"); ok {
					_ = os.WriteFile(metaPath, metaData, 0644)
					_ = os.WriteFile(archivePath, tarData, 0644)
					if err := extractTarGzBytes(tarData, targetDir); err == nil {
						m.recordStat("hits")
						return &rec, true
					}
				}
			}
		}
	}

	m.recordStat("misses")
	return nil, false
}

// PutArtifact packages targetDir into a compressed archive and saves metadata.
func (m *Manager) PutArtifact(key string, sourceDir string, rec *ArtifactRecord) error {
	if !m.Config.Enabled || rec == nil || key == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	archivePath := filepath.Join(m.artifactDir, key+".tar.gz")
	metaPath := filepath.Join(m.artifactDir, key+".meta.json")

	// Compress sourceDir into .tar.gz
	tarBytes, files, err := createTarGz(sourceDir)
	if err != nil {
		return fmt.Errorf("failed to package artifact: %w", err)
	}

	rec.Files = files
	metaData, _ := json.MarshalIndent(rec, "", "  ")

	if err := os.WriteFile(archivePath, tarBytes, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(metaPath, metaData, 0644); err != nil {
		return err
	}

	// Sync to remote cache
	if m.Config.RemoteURL != "" {
		if strings.HasPrefix(m.Config.RemoteURL, "file://") || filepath.IsAbs(m.Config.RemoteURL) {
			_ = m.pushRemote(key, "artifact-meta", metaData)
			_ = m.pushRemote(key, "artifact-tar", tarBytes)
		} else {
			go func(k string, meta []byte, tar []byte) {
				_ = m.pushRemote(k, "artifact-meta", meta)
				_ = m.pushRemote(k, "artifact-tar", tar)
			}(key, metaData, tarBytes)
		}
	}

	return nil
}

// fetchRemote retrieves cached data from an HTTP or directory remote cache.
func (m *Manager) fetchRemote(key string, category string) ([]byte, bool) {
	remote := m.Config.RemoteURL
	if strings.HasPrefix(remote, "file://") || filepath.IsAbs(remote) {
		baseDir := strings.TrimPrefix(remote, "file://")
		filePath := filepath.Join(baseDir, category, key)
		if data, err := os.ReadFile(filePath); err == nil {
			return data, true
		}
		return nil, false
	}

	if strings.HasPrefix(remote, "http://") || strings.HasPrefix(remote, "https://") {
		url := fmt.Sprintf("%s/v8/artifacts/%s?type=%s", strings.TrimSuffix(remote, "/"), key, category)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, false
		}

		if m.Config.RemoteToken != "" {
			req.Header.Set("Authorization", "Bearer "+m.Config.RemoteToken)
		}
		req.Header.Set("User-Agent", "MonoFlow/1.0")

		resp, err := m.httpClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				_ = resp.Body.Close()
			}
			return nil, false
		}
		defer resp.Body.Close()

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, false
		}
		return data, true
	}

	return nil, false
}

// pushRemote uploads data to an HTTP or directory remote cache.
func (m *Manager) pushRemote(key string, category string, data []byte) error {
	remote := m.Config.RemoteURL
	if strings.HasPrefix(remote, "file://") || filepath.IsAbs(remote) {
		baseDir := strings.TrimPrefix(remote, "file://")
		targetDir := filepath.Join(baseDir, category)
		_ = os.MkdirAll(targetDir, 0755)
		return os.WriteFile(filepath.Join(targetDir, key), data, 0644)
	}

	if strings.HasPrefix(remote, "http://") || strings.HasPrefix(remote, "https://") {
		url := fmt.Sprintf("%s/v8/artifacts/%s?type=%s", strings.TrimSuffix(remote, "/"), key, category)
		req, err := http.NewRequest("PUT", url, bytes.NewReader(data))
		if err != nil {
			return err
		}

		if m.Config.RemoteToken != "" {
			req.Header.Set("Authorization", "Bearer "+m.Config.RemoteToken)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("User-Agent", "MonoFlow/1.0")

		resp, err := m.httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return nil
	}

	return nil
}

// Clean purges local cache directories.
func (m *Manager) Clean() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_ = os.RemoveAll(m.verifyDir)
	_ = os.RemoveAll(m.artifactDir)
	_ = os.Remove(filepath.Join(m.LocalDir, "stats.hits"))
	_ = os.Remove(filepath.Join(m.LocalDir, "stats.misses"))
	m.stats.Hits, m.stats.Misses = 0, 0
	_ = os.MkdirAll(m.verifyDir, 0755)
	_ = os.MkdirAll(m.artifactDir, 0755)
	return nil
}

// recordStat persists one hit/miss by appending a byte to a counter file. O_APPEND
// writes are atomic, so concurrent agent processes sharing the cache never lose counts
// (in-memory counters died with each short-lived CLI process).
func (m *Manager) recordStat(kind string) {
	if kind == "hits" {
		m.stats.Hits++
	} else {
		m.stats.Misses++
	}
	f, err := os.OpenFile(filepath.Join(m.LocalDir, "stats."+kind), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	_, _ = f.Write([]byte{'.'})
	_ = f.Close()
}

func (m *Manager) statCount(kind string) int {
	if fi, err := os.Stat(filepath.Join(m.LocalDir, "stats."+kind)); err == nil {
		return int(fi.Size())
	}
	if kind == "hits" {
		return m.stats.Hits
	}
	return m.stats.Misses
}

// GetStats returns usage statistics of the local cache.
func (m *Manager) GetStats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s := m.stats
	s.Hits = m.statCount("hits")
	s.Misses = m.statCount("misses")
	if total := s.Hits + s.Misses; total > 0 {
		s.HitRatio = float64(s.Hits) / float64(total)
	}

	var totalBytes int64
	var artCount int
	var verCount int

	_ = filepath.Walk(m.LocalDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && info.Name() == "baselines" {
			return filepath.SkipDir // baseline checkouts are not cache entries
		}
		if err == nil && !info.IsDir() {
			totalBytes += info.Size()
			if strings.HasSuffix(path, ".tar.gz") {
				artCount++
			} else if strings.HasSuffix(path, ".json") && strings.Contains(path, "verify") {
				verCount++
			}
		}
		return nil
	})

	s.TotalBytes = totalBytes
	s.TotalArtifacts = artCount
	s.TotalVerifies = verCount
	return s
}

// --- Tar.Gz Helpers ---

func createTarGz(sourceDir string) ([]byte, []string, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	var files []string

	err := filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
			files = append(files, header.Name)
		}

		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, nil, err
	}

	return buf.Bytes(), files, nil
}

func extractTarGz(archivePath, targetDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	return extractTarGzReader(f, targetDir)
}

func extractTarGzBytes(data []byte, targetDir string) error {
	return extractTarGzReader(bytes.NewReader(data), targetDir)
}

func extractTarGzReader(r io.Reader, targetDir string) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		targetPath := filepath.Join(targetDir, filepath.FromSlash(header.Name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return err
			}
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		}
	}

	return nil
}
