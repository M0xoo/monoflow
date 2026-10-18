package main

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/antigravity/monoflow/pkg/cache"
)

// handleFind prints where a text occurs in the repository, compactly: source lines with their
// text, test and doc files as line numbers. Repo-wide grep dumps were the largest token item
// in both arms of the benchmarks (one grep: 8.6k tokens, re-read on every later turn).
func handleFind(rootDir string) {
	var pattern string
	fixed := true
	for _, a := range os.Args[2:] {
		switch {
		case a == "--regex" || a == "-E":
			fixed = false
		case pattern == "":
			pattern = a
		}
	}
	if pattern == "" {
		fmt.Println(`usage: monoflow find "<text>" [--regex]`)
		exit(2)
	}
	args := []string{"-C", rootDir, "grep", "-n", "-I", "--untracked", "--no-color"}
	if fixed {
		args = append(args, "-F")
	} else {
		args = append(args, "-E")
	}
	args = append(args, "-e", pattern, "--", ".", ":(exclude).agents")
	out, err := exec.Command("git", args...).Output()
	if err != nil && len(out) == 0 {
		fmt.Printf("%s: no matches\n", pattern)
		return
	}
	fmt.Print(findText(pattern, string(out)))
}

type findHit struct {
	file string
	line int
	text string
}

// findKind sorts a matched file: "skip" for generated or historical files nobody edits by hand.
func findKind(file string) string {
	base := strings.ToLower(path.Base(file))
	switch {
	case strings.HasPrefix(base, "changelog"), strings.Contains(file, "/dist/"), strings.HasPrefix(file, "dist/"),
		strings.Contains(file, "/node_modules/"), strings.HasSuffix(base, ".lock"), base == "pnpm-lock.yaml", base == "package-lock.json",
		strings.Contains(file, "/reference/") && strings.HasSuffix(base, ".md"), strings.HasSuffix(base, ".min.js"), strings.HasSuffix(base, ".map"):
		return "skip"
	case cache.IsTrackedTestFile(file), strings.HasSuffix(base, "_test.go"), testFileName(base), strings.Contains("/"+file, "/__tests__/"), strings.Contains("/"+file, "/test/"), strings.Contains("/"+file, "/tests/"):
		return "tests"
	case strings.HasSuffix(base, ".md"), strings.HasSuffix(base, ".mdx"), strings.HasSuffix(base, ".rst"), strings.HasSuffix(base, ".txt"):
		return "docs"
	case strings.HasPrefix(file, "examples/") || strings.Contains(file, "/examples/"):
		return "examples"
	}
	return "code"
}

func findText(pattern, grepOut string) string {
	byKind := map[string]map[string][]findHit{}
	skipped := 0
	total := 0
	for _, l := range strings.Split(strings.TrimRight(grepOut, "\n"), "\n") {
		parts := strings.SplitN(l, ":", 3)
		if len(parts) != 3 {
			continue
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		total++
		k := findKind(parts[0])
		if k == "skip" {
			skipped++
			continue
		}
		if byKind[k] == nil {
			byKind[k] = map[string][]findHit{}
		}
		byKind[k][parts[0]] = append(byKind[k][parts[0]], findHit{parts[0], n, parts[2]})
	}
	var b strings.Builder
	var counts []string
	for _, k := range []string{"code", "tests", "docs", "examples"} {
		if n := len(byKind[k]); n > 0 {
			counts = append(counts, fmt.Sprintf("%s %d", k, n))
		}
	}
	fmt.Fprintf(&b, "%s: %d matches; files: %s", pattern, total, strings.Join(counts, ", "))
	if skipped > 0 {
		fmt.Fprintf(&b, " (%d in changelogs/generated/build output left out)", skipped)
	}
	b.WriteString("\n")
	shownText := 0
	for _, k := range []string{"code", "tests", "docs", "examples"} {
		files := byKind[k]
		if len(files) == 0 {
			continue
		}
		names := make([]string, 0, len(files))
		for f := range files {
			names = append(names, f)
		}
		sort.Strings(names)
		b.WriteString(k + ":\n")
		for _, f := range names {
			hits := files[f]
			// Source lines with their text (what the change must touch); tests and docs as line
			// numbers (verify names the tests that break; read a file when editing it). With
			// few matches, test lines get their text too: it saves reading each test file.
			if (k == "code" && shownText < 60) || (k == "tests" && total <= 20) {
				for _, h := range hits {
					t := strings.TrimSpace(h.text)
					if len(t) > 140 {
						t = t[:140] + "…"
					}
					fmt.Fprintf(&b, "  %s:%d  %s\n", f, h.line, t)
					shownText++
				}
				continue
			}
			lines := make([]string, len(hits))
			for i, h := range hits {
				lines[i] = strconv.Itoa(h.line)
			}
			if len(lines) > 30 {
				lines = append(lines[:30], fmt.Sprintf("… %d more", len(hits)-30))
			}
			fmt.Fprintf(&b, "  %s:%s\n", f, strings.Join(lines, ","))
		}
	}
	return b.String()
}

// testFileName matches test files named by convention rather than location: test.js,
// test_x.py, x_test.py, x.spec.rb, x_spec.rb.
func testFileName(base string) bool {
	stem := strings.TrimSuffix(base, path.Ext(base))
	return stem == "test" || stem == "tests" || strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, "_test") ||
		strings.HasSuffix(stem, "_spec") || strings.HasSuffix(stem, ".spec") || strings.HasSuffix(stem, ".test")
}
