package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/antigravity/monoflow/pkg/verify"
)

// Output for agents is plain text: an agent reads it, it does not parse it, and JSON's quotes,
// braces, escaped newlines and repeated keys cost tokens on every call. --json (or
// MONOFLOW_FORMAT=json) keeps the structured form for scripts and MonoFlow's own callers.

// jsonMode reports whether structured output was requested.
func jsonMode() bool { return os.Getenv("MONOFLOW_FORMAT") == "json" }

// takeJSONFlag removes --json from os.Args and switches to structured output.
func takeJSONFlag() {
	args := os.Args[:1]
	for _, a := range os.Args[1:] {
		if a == "--json" {
			_ = os.Setenv("MONOFLOW_FORMAT", "json")
			continue
		}
		args = append(args, a)
	}
	os.Args = args
}

// verifyText renders a compact verify result: status line, scope, breaks, notes, next step.
func verifyText(res verify.SuiteResult, c map[string]any) string {
	var b strings.Builder
	status := "PASS"
	if res.Status != "passed" {
		status = "FAIL"
	}
	fmt.Fprintf(&b, "%s %d/%d packages passed", status, res.PassedTargets, res.TotalTargets)
	if res.CachedTargets > 0 {
		fmt.Fprintf(&b, " (%d cached)", res.CachedTargets)
	}
	fmt.Fprintf(&b, " in %.1fs\n", res.DurationMs/1000)
	if s, ok := c["scope"].(string); ok {
		b.WriteString("scope: " + s + "\n")
	}
	if na, ok := c["not_affected"].([]string); ok && len(na) > 0 {
		b.WriteString("not affected: " + strings.Join(na, ", ") + "\n")
	}
	if s, ok := c["excluded"].(string); ok {
		b.WriteString("skipped: " + s + "\n")
	}
	if f, ok := c["formatted"].([]string); ok {
		b.WriteString(fixLines(verify.FixResult{Fixed: f}))
	}
	if f, ok := c["fix_failed"].([]string); ok {
		b.WriteString(fixLines(verify.FixResult{Failed: f}))
	}
	if len(res.Pending) > 0 && !strings.Contains(fmt.Sprint(c["scope"]), "background") {
		fmt.Fprintf(&b, "pending: %d dependents run once the changed packages pass\n", len(res.Pending))
	}
	if failed, ok := c["failed"].(map[string]int); ok && len(failed) > 0 {
		var names []string
		for t := range failed {
			names = append(names, t)
		}
		sort.Strings(names)
		b.WriteString("failed: " + strings.Join(names, ", ") + "\n")
	}
	errs, _ := c["errors"].([]verify.SharedError) // deduplicated by compactSuite
	b.WriteString(failureList(errs))
	snapshots := 0
	for _, e := range errs {
		if strings.Contains(e.Message, "Snapshot") && strings.Contains(e.Message, "mismatched") {
			snapshots++
		}
	}
	if snapshots > 0 {
		fmt.Fprintf(&b, "snapshots: %d mismatched; if the new values are what you intend, 'monoflow verify --update-snapshots' rewrites them\n", snapshots)
	}
	for _, k := range []string{"preexisting", "note", "hint"} {
		if s, ok := c[k].(string); ok {
			b.WriteString(k + ": " + s + "\n")
		}
	}
	if fl, ok := c["flaky"].([]string); ok && len(fl) > 0 {
		b.WriteString("flaky (failed under load, passed alone; not blocking): " + strings.Join(fl, ", ") + "\n")
	}
	if s, ok := c["next"].(string); ok {
		b.WriteString("next: " + s + "\n")
	}
	return b.String()
}

// failureList prints every failure, grouped by file, one line each (the first few with their
// diff and source line). All of them: a list cut at 30 ("... 39 more errors") sent agents to
// verify --verbose and their own vitest runs to see the rest (TanStack Query: 100-210 s a run).
func failureList(errs []verify.SharedError) string {
	if len(errs) == 0 {
		return ""
	}
	var b strings.Builder
	// A runner may report a failed test twice: once located, once by name only. Keep the first.
	located := map[string]bool{}
	for _, e := range errs {
		if e.Line > 0 {
			located[e.File+"\x00"+testNameOf(e.Message)] = true
		}
	}
	var files []string
	byFile := map[string][]verify.SharedError{}
	for _, e := range errs {
		if name := testNameOf(e.Message); e.Line == 0 && name != "" && located[e.File+"\x00"+name] {
			continue
		}
		if _, ok := byFile[e.File]; !ok {
			files = append(files, e.File)
		}
		byFile[e.File] = append(byFile[e.File], e)
	}
	if len(errs) > 1 {
		fmt.Fprintf(&b, "%d failures in %d files:\n", len(errs), len(files))
	}
	detailed := 0
	for _, f := range files {
		list := byFile[f]
		sort.SliceStable(list, func(i, j int) bool { return list[i].Line < list[j].Line })
		if len(list) > 1 {
			fmt.Fprintf(&b, "%s (%d)\n", f, len(list))
		}
		for _, e := range list {
			loc := e.File
			if len(list) > 1 {
				loc = " "
			}
			if e.Line > 0 {
				loc += fmt.Sprintf(":%d", e.Line)
			}
			msg := cleanFailure(e.Message)
			first, rest, _ := strings.Cut(msg, "\n")
			if len(first) > 300 {
				first = first[:300] + "…"
			}
			fmt.Fprintf(&b, "%s %s\n", loc, first)
			if e.Breaks > 2 {
				fmt.Fprintf(&b, "  (same error in %d packages)\n", e.Breaks)
			}
			// The first failures in full (diff, failing line): agents otherwise spend a call
			// reading the file around them; the rest one line each, to keep the list short.
			if detailed < 8 {
				if rest = strings.TrimSpace(rest); rest != "" {
					b.WriteString("  " + strings.ReplaceAll(rest, "\n", "\n  ") + "\n")
				}
				if e.Line > 0 {
					b.WriteString(sourceContext(rootDirForText, e.File, e.Line, 1))
				}
				detailed++
			}
		}
	}
	return b.String()
}

// testNameOf is the "[suite > test]" prefix of a test failure ("" if none).
func testNameOf(msg string) string {
	if strings.HasPrefix(msg, "[") {
		if i := strings.Index(msg, "]"); i > 0 {
			return msg[1:i]
		}
	}
	return ""
}

// matcherBoilerplate is advice testing libraries append to every failure of a kind; it says
// nothing about the failure and cost TanStack Query runs 25-60k tokens of context.
var matcherBoilerplate = []string{
	" This could be because the text is broken up by multiple elements. In this case, you can provide a function for your text matcher to make your matcher more flexible.",
	"This could be because the text is broken up by multiple elements. In this case, you can provide a function for your text matcher to make your matcher more flexible.",
}

func cleanFailure(msg string) string {
	msg = strings.TrimSpace(msg)
	for _, p := range matcherBoilerplate {
		msg = strings.ReplaceAll(msg, p, "")
	}
	// Testing Library dumps the whole rendered DOM after its message.
	for _, cut := range []string{"\n\nIgnored nodes:", "\n<body>", "; <body>", " <body>"} {
		if i := strings.Index(msg, cut); i > 0 {
			msg = strings.TrimSpace(msg[:i])
		}
	}
	return msg
}

// kvText renders a flat result as "key: value" lines, skipping empty values; lists are joined.
func kvText(m map[string]any, order ...string) string {
	var b strings.Builder
	seen := map[string]bool{}
	write := func(k string, v any) {
		switch x := v.(type) {
		case nil:
			return
		case string:
			if x == "" {
				return
			}
		case []string:
			if len(x) == 0 {
				return
			}
			v = strings.Join(x, ", ")
		case []any:
			if len(x) == 0 {
				return
			}
			parts := make([]string, len(x))
			for i, e := range x {
				parts[i] = fmt.Sprint(e)
			}
			v = strings.Join(parts, ", ")
		case map[string]any:
			if len(x) == 0 {
				return
			}
			keys := make([]string, 0, len(x))
			for kk := range x {
				keys = append(keys, kk)
			}
			sort.Strings(keys)
			parts := make([]string, len(keys))
			for i, kk := range keys {
				parts[i] = fmt.Sprintf("%s=%v", kk, x[kk])
			}
			v = strings.Join(parts, " ")
		}
		fmt.Fprintf(&b, "%s: %v\n", k, v)
	}
	for _, k := range order {
		if v, ok := m[k]; ok {
			write(k, v)
			seen[k] = true
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		write(k, m[k])
	}
	return b.String()
}

// emit prints v as JSON when requested, else as text.
func emit(v map[string]any, order ...string) {
	if jsonMode() {
		printJSON(v)
		return
	}
	fmt.Print(kvText(v, order...))
}

// printOut prints a command result: JSON with --json, else compact text (see renderText).
func printOut(v any) {
	data, _ := json.MarshalIndent(v, "", "  ")
	if jsonMode() {
		fmt.Println(string(data))
		return
	}
	var node any
	if json.Unmarshal(data, &node) != nil {
		fmt.Println(string(data))
		return
	}
	var b strings.Builder
	renderText(&b, node, "")
	fmt.Print(b.String())
}

// renderText writes a JSON value as indented "key: value" text: status-like keys first, empty
// values dropped, scalar lists on one line, objects in lists as "- " items.
func renderText(b *strings.Builder, node any, indent string) {
	switch x := node.(type) {
	case map[string]any:
		for _, k := range orderedKeys(x) {
			v := x[k]
			if isEmpty(v) {
				continue
			}
			switch vv := v.(type) {
			case map[string]any:
				fmt.Fprintf(b, "%s%s:\n", indent, k)
				renderText(b, vv, indent+"  ")
			case []any:
				if allScalars(vv) {
					fmt.Fprintf(b, "%s%s: %s\n", indent, k, joinScalars(vv))
				} else {
					fmt.Fprintf(b, "%s%s:\n", indent, k)
					renderText(b, vv, indent+"  ")
				}
			default:
				fmt.Fprintf(b, "%s%s: %s\n", indent, k, scalarText(vv, indent+"  "))
			}
		}
	case []any:
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				var sub strings.Builder
				renderText(&sub, m, indent+"  ")
				fmt.Fprintf(b, "%s- %s", indent, strings.TrimPrefix(sub.String(), indent+"  "))
			} else {
				fmt.Fprintf(b, "%s- %s\n", indent, scalarText(e, indent+"  "))
			}
		}
	default:
		fmt.Fprintf(b, "%s%s\n", indent, scalarText(x, indent))
	}
}

func orderedKeys(m map[string]any) []string {
	first := []string{"status", "error", "id", "path", "branch", "commit", "package"}
	var keys []string
	for _, k := range first {
		if _, ok := m[k]; ok {
			keys = append(keys, k)
		}
	}
	var rest []string
	for k := range m {
		isFirst := false
		for _, f := range first {
			isFirst = isFirst || k == f
		}
		if !isFirst {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	case bool:
		return !x
	}
	return false
}

func allScalars(xs []any) bool {
	for _, x := range xs {
		switch x.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

func joinScalars(xs []any) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = scalarText(x, "")
	}
	return strings.Join(parts, ", ")
}

func scalarText(v any, indent string) string {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(strings.TrimRight(x, "\n"), "\n", "\n"+indent)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%.1f", x)
	}
	return fmt.Sprint(v)
}

// printRaw prints a hand-built JSON message (mostly errors) in the active format, so success
// and error output look the same; a string that is not valid JSON is printed as is.
func printRaw(s string) {
	var node any
	if json.Unmarshal([]byte(s), &node) != nil {
		fmt.Println(s)
		return
	}
	printOut(node)
}

// rootDirForText lets verifyText read source lines (set by handleVerify).
var rootDirForText string

// sourceContext returns lines [line-radius, line+radius] of root/file, numbered, the failing
// line marked with '>'; "" when the file cannot be read.
func sourceContext(root, file string, line, radius int) string {
	data, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if line > len(lines) {
		return ""
	}
	var b strings.Builder
	for n := max(1, line-radius); n <= min(len(lines), line+radius); n++ {
		mark := " "
		if n == line {
			mark = ">"
		}
		text := strings.TrimRight(lines[n-1], " \t")
		if len(text) > 160 {
			text = text[:160] + "…"
		}
		fmt.Fprintf(&b, "  %s%5d| %s\n", mark, n, text)
	}
	return b.String()
}
