package verify

import (
	"os"
	"path/filepath"
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

	goPkg string // import path from go test's "FAIL <pkg>" line, to place a bare file name
}

// StripANSI removes all ANSI escape sequences, spinners, and carriage returns.
func StripANSI(input string) string {
	// ANSI escape codes regex (colors, cursor movements, erase line)
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\].*?\x07|\x1b[()][AB012]`)
	cleaned := ansiRegex.ReplaceAllString(input, "")

	// Remove carriage returns and spinner artifacts
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")
	spinnerRegex := regexp.MustCompile(`[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏✔✖○◐◑◒◓]\s*`)
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
	goTestRegex := regexp.MustCompile(`^([^\s:]+_test\.go):(\d+):\s*(.*)$`)
	goFailRegex := regexp.MustCompile(`^--- FAIL:\s+(\S+)`)
	goPkgFailRegex := regexp.MustCompile(`^FAIL\s+(\S+)\s+(\[|[\d.]+s)`)
	pkgStart := 0 // first diagnostic not yet tied to a go package
	// vitest: " FAIL  |proj| src/a.test.ts > suite > test", the error, " ❯ src/a.test.ts:285:22".
	// jest:   "● suite › test", the error, "at Object.<anonymous> (src/a.test.ts:12:5)".
	vitestFailRegex := regexp.MustCompile(`^FAIL\s+(?:\|[^|]+\|\s+)?(\S+\.(?:test|spec)\.[cm]?[jt]sx?)\s+>\s+(.+)$`)
	jestFailRegex := regexp.MustCompile(`^● (.+ › .+)$`)
	jsLocRegex := regexp.MustCompile(`(?:❯\s+|\()([^\s()]+\.[cm]?[jt]sx?):(\d+):(\d+)\)?$`)
	jsTest, jsHasMsg := -1, false // diagnostic being filled from following lines

	// 3. Rust: error[E0308]: message ... --> path/file.rs:28:14
	rustLocRegex := regexp.MustCompile(`^\s*-->\s*(.+?\.rs):(\d+):(\d+)`)
	rustErrRegex := regexp.MustCompile(`^error\[([A-Z0-9]+)\]:\s*(.+)`)

	// 4. Python (Ruff / Flake8 / Mypy): path/file.py:42:5: E501 message
	pyRegex := regexp.MustCompile(`^(.+?\.py):(\d+):(\d+):\s*([A-Za-z0-9]+)\s*(.+)`)
	pyMypyRegex := regexp.MustCompile(`^(.+?\.py):(\d+):\s*error:\s*(.+)`)

	// Ruff's default ("full") output: "F401 [*] `os` imported but unused", then " --> a.py:1:8".
	ruffHeadRegex := regexp.MustCompile(`^([A-Z]{1,4}[0-9]{1,4}|invalid-syntax):?\s+(?:\[\*\]\s+)?(.+)`)
	pyLocRegex := regexp.MustCompile(`^-->\s*(.+?\.pyi?):(\d+):(\d+)`)
	var ruffCode, ruffMsg string

	// 5. Pytest: each failure ends with its location ("tests/test_a.py:5: AssertionError",
	// after the "E   assert 4 == 3" lines); the summary then names the tests, in the same order:
	// "FAILED tests/test_a.py::test_add - assert 4 == 3", "ERROR tests/test_b.py - ImportError…".
	pytestLocRegex := regexp.MustCompile(`^(\S+\.py):(\d+):\s+([A-Za-z_][A-Za-z0-9_.]*)$`)
	pytestSummaryRegex := regexp.MustCompile(`^(FAILED|ERROR)\s+(\S+?\.py)(?:::(\S+))?(?:\s+-\s+(.+))?$`)
	var pytestE string   // first "E   ..." line of the failure being printed
	var pytestLocs []int // failures located but not yet named by the summary
	// A module that fails to import: "___ ERROR collecting tests/test_a.py ___", its traceback
	// ("src/a/__init__.py:1: in <module>"), "E   ImportError: …"; the summary repeats only the file.
	pytestCollectRegex := regexp.MustCompile(`^_+ ERROR collecting (\S+) _+$`)
	pytestFrameRegex := regexp.MustCompile(`^(\S+\.py):(\d+): in `)
	pytestCollecting := -1
	pytestCollected := map[string]bool{}

	// 6. Generic path:line:col or path:line error
	genericRegex := regexp.MustCompile(`^([a-zA-Z0-9_./\-]+):(\d+)(?::(\d+))?:\s*(?:error:?\s*)?(.+)`)

	var lastRustCode string
	var lastRustMsg string
	var lastGoTest string
	// testify prints "x_test.go:110:" then the message on following lines ("Error: Not equal:",
	// a diff): fold them into that diagnostic.
	testify, inDiff, diffLines := -1, false, 0

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

		if jsTest >= 0 {
			d := &errors[jsTest]
			switch {
			case strings.HasPrefix(trimmed, "⎯") || vitestFailRegex.MatchString(trimmed) || jestFailRegex.MatchString(trimmed):
				jsTest = -1 // next failure or end of this one: handled below
			default:
				if m := jsLocRegex.FindStringSubmatch(trimmed); m != nil {
					if d.Line == 0 && !strings.Contains(m[1], "node_modules") {
						d.File = strings.TrimPrefix(m[1], "./")
						d.Line, _ = strconv.Atoi(m[2])
						d.Col, _ = strconv.Atoi(m[3])
					}
					continue
				}
				switch {
				case !jsHasMsg && !strings.HasPrefix(trimmed, "at "):
					d.Message += trimmed // the error line ("AssertionError: expected …")
					jsHasMsg = true
				case jsHasMsg && (strings.HasPrefix(trimmed, "Expected") || strings.HasPrefix(trimmed, "Received")) && len(trimmed) <= 200:
					d.Message += "; " + trimmed
				}
				continue
			}
		}
		if m := vitestFailRegex.FindStringSubmatch(trimmed); m != nil {
			errors = append(errors, DiagnosticError{File: m[1], Code: "test-failure", Message: "[" + m[2] + "] "})
			jsTest, jsHasMsg = len(errors)-1, false
			continue
		}
		if m := jestFailRegex.FindStringSubmatch(trimmed); m != nil {
			errors = append(errors, DiagnosticError{Code: "test-failure", Message: "[" + m[1] + "] "})
			jsTest, jsHasMsg = len(errors)-1, false
			continue
		}

		if testify >= 0 {
			d := &errors[testify]
			switch {
			case strings.HasPrefix(trimmed, "Error Trace:"), strings.HasPrefix(trimmed, "Test:"):
				continue
			case strings.HasPrefix(trimmed, "Error:"):
				d.Message += strings.TrimSpace(strings.TrimPrefix(trimmed, "Error:"))
				continue
			case strings.HasPrefix(trimmed, "Messages:"):
				d.Message += " (" + strings.TrimSpace(strings.TrimPrefix(trimmed, "Messages:")) + ")"
				continue
			case trimmed == "Diff:":
				inDiff = true
				continue
			case inDiff && (strings.HasPrefix(trimmed, "---") || strings.HasPrefix(trimmed, "+++") || strings.HasPrefix(trimmed, "@@")):
				continue
			case inDiff && (strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "+")):
				if diffLines < 6 {
					d.Message += "\n" + trimmed
				}
				diffLines++
				continue
			case strings.HasPrefix(trimmed, "expected") || strings.HasPrefix(trimmed, "actual"):
				if !strings.Contains(d.Message, "\n") && len(trimmed) <= 200 {
					d.Message += " " + trimmed
				}
				continue
			case !strings.HasPrefix(trimmed, "--- FAIL") && !strings.HasPrefix(trimmed, "FAIL") &&
				!strings.HasPrefix(trimmed, "ok ") && goTestRegex.FindStringSubmatch(trimmed) == nil:
				continue // diff context lines
			}
			testify, inDiff, diffLines = -1, false, 0
		}

		if matches := goPkgFailRegex.FindStringSubmatch(trimmed); matches != nil {
			for i := pkgStart; i < len(errors); i++ {
				errors[i].goPkg = matches[1]
			}
			pkgStart = len(errors)
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
			if strings.TrimSpace(matches[3]) == "" {
				testify = len(errors) - 1
			}
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

		// Ruff full output: header, then its location.
		if ruffCode != "" {
			if m := pyLocRegex.FindStringSubmatch(trimmed); m != nil {
				lineNum, _ := strconv.Atoi(m[2])
				colNum, _ := strconv.Atoi(m[3])
				errors = append(errors, DiagnosticError{File: m[1], Line: lineNum, Col: colNum, Code: ruffCode, Message: ruffMsg})
				ruffCode, ruffMsg = "", ""
				continue
			}
			ruffCode, ruffMsg = "", ""
		}
		if m := ruffHeadRegex.FindStringSubmatch(trimmed); m != nil && !strings.Contains(trimmed, ".py:") {
			ruffCode, ruffMsg = m[1], strings.TrimSpace(m[2])
			continue
		}

		// Pytest
		if m := pytestCollectRegex.FindStringSubmatch(trimmed); m != nil {
			errors = append(errors, DiagnosticError{File: m[1], Line: 1, Code: "test-error", Message: "[" + m[1] + "] "})
			pytestCollecting, pytestCollected[m[1]], pytestE = len(errors)-1, true, ""
			continue
		}
		if strings.HasPrefix(line, "E  ") {
			if pytestE == "" {
				pytestE = strings.TrimSpace(trimmed[1:])
				if pytestCollecting >= 0 {
					errors[pytestCollecting].Message += pytestE
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "___") || strings.HasPrefix(trimmed, "===") {
			pytestE, pytestCollecting = "", -1
			continue
		}
		if pytestCollecting >= 0 {
			// The innermost frame in the project (not the interpreter's importlib) is the culprit.
			if m := pytestFrameRegex.FindStringSubmatch(trimmed); m != nil && !filepath.IsAbs(m[1]) && !strings.Contains(m[1], "site-packages") {
				errors[pytestCollecting].File = m[1]
				errors[pytestCollecting].Line, _ = strconv.Atoi(m[2])
			}
			continue
		}
		if m := pytestLocRegex.FindStringSubmatch(trimmed); m != nil {
			lineNum, _ := strconv.Atoi(m[2])
			msg := m[3]
			if pytestE != "" {
				msg = pytestE
			}
			errors = append(errors, DiagnosticError{File: m[1], Line: lineNum, Code: "test-failure", Message: msg})
			pytestLocs = append(pytestLocs, len(errors)-1)
			pytestE = ""
			continue
		}
		if m := pytestSummaryRegex.FindStringSubmatch(trimmed); m != nil {
			if m[1] == "ERROR" && m[3] == "" && pytestCollected[m[2]] {
				continue // reported from its "ERROR collecting" section
			}
			test, msg := m[3], m[4]
			if test == "" {
				test = m[2]
			}
			if m[1] == "FAILED" && len(pytestLocs) > 0 {
				d := &errors[pytestLocs[0]]
				pytestLocs = pytestLocs[1:]
				if msg != "" {
					d.Message = msg
				}
				d.Message = "[" + test + "] " + d.Message
				continue
			}
			if msg == "" {
				msg = pytestE
			}
			if msg == "" {
				msg = strings.ToLower(m[1])
			}
			code := "test-failure"
			if m[1] == "ERROR" {
				code = "test-error"
			}
			errors = append(errors, DiagnosticError{File: m[2], Line: 1, Code: code, Message: "[" + test + "] " + msg})
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
				Message: strings.TrimPrefix(strings.TrimSpace(matches[5]), "[*] "),
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

// ResolveGoTestFiles turns bare test file names (go test prints "codes_test.go:25") into paths
// relative to the module dir, using the import path of the failing package and the module path.
func ResolveGoTestFiles(errs []DiagnosticError, moduleDir string) {
	data, err := os.ReadFile(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return
	}
	var mod string
	for _, l := range strings.Split(string(data), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "module" {
			mod = strings.Trim(f[1], `"`)
			break
		}
	}
	for i := range errs {
		e := &errs[i]
		if mod == "" || e.goPkg == "" || strings.ContainsAny(e.File, "/\\") {
			continue
		}
		if e.goPkg == mod {
			continue
		}
		if rel, ok := strings.CutPrefix(e.goPkg, mod+"/"); ok {
			e.File = rel + "/" + e.File
		}
	}
}
