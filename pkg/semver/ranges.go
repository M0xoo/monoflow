package semver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Ecosystem selects the range dialect used by a manifest.
type Ecosystem string

const (
	EcoNPM    Ecosystem = "npm"
	EcoCargo  Ecosystem = "cargo"
	EcoPython Ecosystem = "python"
	EcoGo     Ecosystem = "go"
)

type cmpOp struct {
	op string // "=", ">", ">=", "<", "<=", "!="
	v  SemVer
}

// Satisfies reports whether version v satisfies constraint c in the given ecosystem.
//
//   - npm:    ^ ~ x * >= > <= < = exact, space-AND, "||"-OR, hyphen ranges, "workspace:" prefix
//   - cargo:  comma-AND list; a bare "1.2" means "^1.2" (Cargo's default caret)
//   - python: PEP 440 specifiers ">=0.3,<1", "==1.2.*", "~=1.4", "!=1.5"
//   - go:     an exact module version (require v1.4.0); major versions live in the path
//
// An empty constraint (or "*", "workspace:*") is always satisfied.
func Satisfies(eco Ecosystem, c, v string) (bool, error) {
	ver, err := Parse(v)
	if err != nil {
		return false, err
	}
	c = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c), "workspace:"))
	if c == "" || c == "*" || c == "x" || c == "latest" {
		return true, nil
	}
	if eco == EcoNPM && (c == "^" || c == "~") {
		return true, nil // workspace:^ / workspace:~ track the local version
	}
	for _, alt := range strings.Split(c, "||") {
		ops, err := parseAlternative(eco, strings.TrimSpace(alt))
		if err != nil {
			return false, err
		}
		ok := true
		for _, o := range ops {
			if !o.match(ver) {
				ok = false
				break
			}
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (o cmpOp) match(v *SemVer) bool {
	c := compare(*v, o.v)
	switch o.op {
	case "=":
		return c == 0
	case "!=":
		return c != 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	}
	return false
}

func compare(a, b SemVer) int {
	for _, d := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

var opRe = regexp.MustCompile(`^(\^|~=|~|>=|<=|==|!=|>|<|=)?\s*v?([0-9xX*]+(?:\.[0-9xX*]+){0,2})(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func parseAlternative(eco Ecosystem, alt string) ([]cmpOp, error) {
	if alt == "" || alt == "*" {
		return nil, nil
	}
	// npm hyphen range "1.2.3 - 2.3.4"
	if eco == EcoNPM && strings.Contains(alt, " - ") {
		parts := strings.SplitN(alt, " - ", 2)
		lo, err := parseOp(eco, ">="+strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, err
		}
		hi, err := parseOp(eco, "<="+strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, err
		}
		return append(lo, hi...), nil
	}
	var tokens []string
	switch eco {
	case EcoCargo, EcoPython:
		tokens = strings.Split(alt, ",")
	default:
		// npm allows ">= 1.2" with a space between operator and version.
		alt = regexp.MustCompile(`(>=|<=|>|<|=|\^|~)\s+`).ReplaceAllString(alt, "$1")
		tokens = strings.Fields(alt)
	}
	var ops []cmpOp
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		o, err := parseOp(eco, t)
		if err != nil {
			return nil, err
		}
		ops = append(ops, o...)
	}
	return ops, nil
}

// parseOp expands one comparator into primitive comparisons.
func parseOp(eco Ecosystem, tok string) ([]cmpOp, error) {
	m := opRe.FindStringSubmatch(strings.TrimSpace(tok))
	if m == nil {
		return nil, fmt.Errorf("unsupported version constraint %q", tok)
	}
	op, raw := m[1], m[2]
	parts := strings.Split(raw, ".")
	nums := []int{}
	for _, p := range parts {
		if p == "x" || p == "X" || p == "*" {
			break
		}
		n, _ := strconv.Atoi(p)
		nums = append(nums, n)
	}
	wild := len(nums) < 3 // "1.2", "1.x", "1.2.*"
	base := SemVer{}
	if len(nums) > 0 {
		base.Major = nums[0]
	}
	if len(nums) > 1 {
		base.Minor = nums[1]
	}
	if len(nums) > 2 {
		base.Patch = nums[2]
	}
	if len(nums) == 0 {
		return nil, nil // "*"
	}
	if op == "" {
		switch {
		case eco == EcoCargo:
			op = "^" // Cargo: bare requirement is a caret requirement
		case eco == EcoGo:
			op = ">=" // Go: minimal version selection, require is a lower bound
		case wild:
			op = "=" // npm "1.2" / "1.x" means the whole 1.2.* range
		default:
			op = "="
		}
	}
	if op == "==" {
		op = "="
	}
	upper := func(v SemVer) cmpOp { return cmpOp{"<", v} }
	switch op {
	case "^":
		// Caret: compatible within the left-most non-zero component.
		switch {
		case base.Major > 0 || len(nums) == 1:
			return []cmpOp{{">=", base}, upper(SemVer{Major: base.Major + 1})}, nil
		case base.Minor > 0 || len(nums) == 2:
			return []cmpOp{{">=", base}, upper(SemVer{Major: 0, Minor: base.Minor + 1})}, nil
		default:
			return []cmpOp{{">=", base}, upper(SemVer{Patch: base.Patch + 1})}, nil
		}
	case "~":
		if len(nums) == 1 {
			return []cmpOp{{">=", base}, upper(SemVer{Major: base.Major + 1})}, nil
		}
		return []cmpOp{{">=", base}, upper(SemVer{Major: base.Major, Minor: base.Minor + 1})}, nil
	case "~=":
		// PEP 440 compatible release: ~=1.4.5 -> >=1.4.5,<1.5 ; ~=1.4 -> >=1.4,<2
		if len(nums) >= 3 {
			return []cmpOp{{">=", base}, upper(SemVer{Major: base.Major, Minor: base.Minor + 1})}, nil
		}
		return []cmpOp{{">=", base}, upper(SemVer{Major: base.Major + 1})}, nil
	case "=":
		if wild {
			if len(nums) == 1 {
				return []cmpOp{{">=", base}, upper(SemVer{Major: base.Major + 1})}, nil
			}
			return []cmpOp{{">=", base}, upper(SemVer{Major: base.Major, Minor: base.Minor + 1})}, nil
		}
		return []cmpOp{{"=", base}}, nil
	case "!=":
		return []cmpOp{{"!=", base}}, nil
	case "<=", "<", ">", ">=":
		if wild && (op == "<=" || op == ">") {
			// "<=1.2" includes all of 1.2.x; ">1.2" excludes them.
			next := SemVer{Major: base.Major, Minor: base.Minor + 1}
			if len(nums) == 1 {
				next = SemVer{Major: base.Major + 1}
			}
			if op == "<=" {
				return []cmpOp{upper(next)}, nil
			}
			return []cmpOp{{">=", next}}, nil
		}
		return []cmpOp{{op, base}}, nil
	}
	return nil, fmt.Errorf("unsupported operator %q", op)
}
