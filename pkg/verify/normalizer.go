package verify

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DiagnosticError represents a standardized, token-compact error.
type DiagnosticError struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col,omitempty"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
	Expected string `json:"expected,omitempty"`
	Received string `json:"received,omitempty"`
}

// StripANSI removes all ANSI escape sequences, spinners, and carriage returns.
func StripANSI(input string) string {
	// ANSI escape codes regex (colors, cursor movements, erase line)
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\].*?\x07|\x1b[()][AB012]`)
	cleaned := ansiRegex.ReplaceAllString(input, "")

	// Remove carriage returns and spinner artifacts
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")
	spinnerRegex := regexp.MustCompile(`[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏✔✖●○◐◑◒◓]\s*`)
	cleaned = spinnerRegex.ReplaceAllString(cleaned, "")

	return cleaned
}

// ParseDiagnostics inspects raw compiler/linter/test output and extracts structured errors.
func ParseDiagnostics(rawOutput string) []DiagnosticError {
	clean := StripANSI(rawOutput)
	lines := strings.Split(clean, "\n")
	var errors []DiagnosticError

	// Regex patterns for different compilers

	// 1. TypeScript: path/file.ts(42,11): error TS2322: Message
	// or: path/file.ts:42:11 - error TS2322: Message
	tsRegex1 := regexp.MustCompile(`^(.+?):(\d+):(\d+)\s*-\s*error\s+([A-Z0-9]+):\s*(.+)`)
	tsRegex2 := regexp.MustCompile(`^(.+?)\((\d+),(\d+)\):\s*error\s+([A-Z0-9]+):\s*(.+)`)

	// 2. Go: path/file.go:77:12: message
	goRegex := regexp.MustCompile(`^(.+?\.go):(\d+):(\d+):\s*(.+)`)
	// Go test assertion (t.Fatal/t.Error), indented under "--- FAIL: TestName": local_test.go:11: msg
	goTestRegex := regexp.MustCompile(`^([^\s:]+_test\.go):(\d+):\s*(.+)`)
	goFailRegex := regexp.MustCompile(`^--- FAIL:\s+(\S+)`)

	// 3. Rust: error[E0308]: message ... --> path/file.rs:28:14
	rustLocRegex := regexp.MustCompile(`^\s*-->\s*(.+?\.rs):(\d+):(\d+)`)
	rustErrRegex := regexp.MustCompile(`^error\[([A-Z0-9]+)\]:\s*(.+)`)

	// 4. Python (Ruff / Flake8 / Mypy): path/file.py:42:5: E501 message
	pyRegex := regexp.MustCompile(`^(.+?\.py):(\d+):(\d+):\s*([A-Za-z0-9]+)\s*(.+)`)
	pyMypyRegex := regexp.MustCompile(`^(.+?\.py):(\d+):\s*error:\s*(.+)`)

	// 5. Pytest failure line: FAILED test_foo.py::test_bar - AssertionError: message
	pytestRegex := regexp.MustCompile(`^FAILED\s+(.+?)::(.+?)\s*-\s*([A-Za-z0-9_]+Error):\s*(.+)`)

	// 6. Generic path:line:col or path:line error
	genericRegex := regexp.MustCompile(`^([a-zA-Z0-9_./\-]+):(\d+)(?::(\d+))?:\s*(?:error:?\s*)?(.+)`)

	var lastRustCode string
	var lastRustMsg string
	var lastGoTest string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// go vet prefixes the first finding of a package: "vet: ./record_test.go:54:3: ...".
		trimmed = strings.TrimPrefix(trimmed, "vet: ")

		// Try TypeScript 1
		if matches := tsRegex1.FindStringSubmatch(trimmed); matches != nil {
			lineNum, _ := strconv.Atoi(matches[2])
			colNum, _ := strconv.Atoi(matches[3])
			err := DiagnosticError{
				File:    strings.TrimSpace(matches[1]),
				Line:    lineNum,
				Col:     colNum,
				Code:    matches[4],
				Message: strings.TrimSpace(matches[5]),
			}
			extractExpectedReceived(&err)
			errors = append(errors, err)
			continue
		}

		// Try TypeScript 2
		if matches := tsRegex2.FindStringSubmatch(trimmed); matches != nil {
			lineNum, _ := strconv.Atoi(matches[2])
			colNum, _ := strconv.Atoi(matches[3])
			err := DiagnosticError{
				File:    strings.TrimSpace(matches[1]),
				Line:    lineNum,
				Col:     colNum,
				Code:    matches[4],
				Message: strings.TrimSpace(matches[5]),
			}
			extractExpectedReceived(&err)
			errors = append(errors, err)
			continue
		}

		// Try Go
		if matches := goRegex.FindStringSubmatch(trimmed); matches != nil {
			lineNum, _ := strconv.Atoi(matches[2])
			colNum, _ := strconv.Atoi(matches[3])
			errors = append(errors, DiagnosticError{
				File:    strings.TrimSpace(matches[1]),
				Line:    lineNum,
				Col:     colNum,
				Code:    "go-error",
				Message: strings.TrimSpace(matches[4]),
			})
			continue
		}

		if matches := goFailRegex.FindStringSubmatch(trimmed); matches != nil {
			lastGoTest = matches[1]
			continue
		}
		if matches := goTestRegex.FindStringSubmatch(trimmed); matches != nil {
			lineNum, _ := strconv.Atoi(matches[2])
			msg := strings.TrimSpace(matches[3])
			if lastGoTest != "" {
				msg = "[" + lastGoTest + "] " + msg
			}
			errors = append(errors, DiagnosticError{
				File:    matches[1],
				Line:    lineNum,
				Code:    "test-failure",
				Message: msg,
			})
			continue
		}

		// Try Rust Header
		if matches := rustErrRegex.FindStringSubmatch(trimmed); matches != nil {
			lastRustCode = matches[1]
			lastRustMsg = matches[2]
			continue
		}

		// Try Rust Location pointer
		if matches := rustLocRegex.FindStringSubmatch(trimmed); matches != nil {
			lineNum, _ := strconv.Atoi(matches[2])
			colNum, _ := strconv.Atoi(matches[3])
			code := lastRustCode
			if code == "" {
				code = "rustc-error"
			}
			msg := lastRustMsg
			if msg == "" {
				msg = "compilation error"
			}
			errors = append(errors, DiagnosticError{
				File:    strings.TrimSpace(matches[1]),
				Line:    lineNum,
				Col:     colNum,
				Code:    code,
				Message: msg,
			})
			lastRustCode = ""
			lastRustMsg = ""
			continue
		}

		// Try Python (Ruff / Flake8)
		if matches := pyRegex.FindStringSubmatch(trimmed); matches != nil {
			lineNum, _ := strconv.Atoi(matches[2])
			colNum, _ := strconv.Atoi(matches[3])
			errors = append(errors, DiagnosticError{
				File:    strings.TrimSpace(matches[1]),
				Line:    lineNum,
				Col:     colNum,
				Code:    matches[4],
				Message: strings.TrimSpace(matches[5]),
			})
			continue
		}

		// Try Python (Mypy)
		if matches := pyMypyRegex.FindStringSubmatch(trimmed); matches != nil {
			lineNum, _ := strconv.Atoi(matches[2])
			errors = append(errors, DiagnosticError{
				File:    strings.TrimSpace(matches[1]),
				Line:    lineNum,
				Code:    "type-error",
				Message: strings.TrimSpace(matches[3]),
			})
			continue
		}

		// Try Pytest
		if matches := pytestRegex.FindStringSubmatch(trimmed); matches != nil {
			errors = append(errors, DiagnosticError{
				File:    strings.TrimSpace(matches[1]),
				Line:    1,
				Code:    matches[3],
				Message: fmt.Sprintf("[%s] %s", matches[2], matches[4]),
			})
			continue
		}

		// Try Generic fallback if line contains 'error:' or file extension
		if strings.Contains(strings.ToLower(trimmed), "error") {
			if matches := genericRegex.FindStringSubmatch(trimmed); matches != nil {
				lineNum, _ := strconv.Atoi(matches[2])
				colNum := 0
				if matches[3] != "" {
					colNum, _ = strconv.Atoi(matches[3])
				}
				errors = append(errors, DiagnosticError{
					File:    strings.TrimSpace(matches[1]),
					Line:    lineNum,
					Col:     colNum,
					Code:    "error",
					Message: strings.TrimSpace(matches[4]),
				})
			}
		}
	}

	return errors
}

func extractExpectedReceived(err *DiagnosticError) {
	// Type 'A' is not assignable to type 'B'.
	re := regexp.MustCompile(`Type '([^']+)' is not assignable to type '([^']+)'`)
	if m := re.FindStringSubmatch(err.Message); m != nil {
		err.Received = m[1]
		err.Expected = m[2]
	}
}

// CompactFailure reduces raw tool output to the lines that explain a failure, so agents
// get the failing test/assertion instead of a full verbose log (context is expensive).
func CompactFailure(raw string) string {
	const maxLen = 1500
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	keep := make([]string, 0)
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "=== RUN") || strings.HasPrefix(t, "=== PAUSE") || strings.HasPrefix(t, "=== CONT") ||
			strings.HasPrefix(t, "--- PASS") || strings.HasPrefix(t, "PASS") || strings.HasPrefix(t, "ok ") || strings.HasPrefix(t, "?   ") {
			continue
		}
		keep = append(keep, l)
	}
	out := strings.Join(keep, "\n")
	if out == "" {
		out = strings.TrimSpace(raw)
	}
	if len(out) > maxLen {
		out = "…" + out[len(out)-maxLen:]
	}
	return out
}
