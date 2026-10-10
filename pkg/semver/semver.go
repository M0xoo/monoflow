// Package semver holds semantic versions and version-range evaluation shared by the
// dependency graph (edge modes) and versioning.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// SemVer represents a semantic version (major.minor.patch).
type SemVer struct {
	Major int
	Minor int
	Patch int
	Raw   string
}

// Parse parses a SemVer string, ignoring optional leading 'v'.
func Parse(v string) (*SemVer, error) {
	clean := strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(clean, ".")
	if len(parts) < 3 {
		// Try fallback like 1.0 -> 1.0.0
		for len(parts) < 3 {
			parts = append(parts, "0")
		}
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid major version in %q: %w", v, err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid minor version in %q: %w", v, err)
	}

	// In case of patch with prerelease (e.g. 0-alpha)
	patchStr := parts[2]
	if idx := strings.IndexAny(patchStr, "-+"); idx != -1 {
		patchStr = patchStr[:idx]
	}
	patch, err := strconv.Atoi(patchStr)
	if err != nil {
		return nil, fmt.Errorf("invalid patch version in %q: %w", v, err)
	}

	return &SemVer{
		Major: major,
		Minor: minor,
		Patch: patch,
		Raw:   v,
	}, nil
}

// String returns formatted SemVer.
func (s *SemVer) String() string {
	return fmt.Sprintf("%d.%d.%d", s.Major, s.Minor, s.Patch)
}

// Bump increments version by bumpType ("patch", "minor", "major").
func (s *SemVer) Bump(bumpType string) error {
	switch strings.ToLower(bumpType) {
	case "patch":
		s.Patch++
	case "minor":
		s.Minor++
		s.Patch = 0
	case "major":
		s.Major++
		s.Minor = 0
		s.Patch = 0
	default:
		return fmt.Errorf("unknown bump type %q (must be 'patch', 'minor', or 'major')", bumpType)
	}
	return nil
}
