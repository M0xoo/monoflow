package main

import (
	"encoding/json"
	"fmt"
	"os"
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
	if len(res.Pending) > 0 {
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
	for _, e := range errs {
		loc := e.File
		if e.Line > 0 {
			loc = fmt.Sprintf("%s:%d", e.File, e.Line)
		}
		msg := strings.TrimSpace(e.Message)
		if i := strings.Index(msg, "\n"); i >= 0 { // diff lines go below, indented
			msg = msg[:i] + "\n  " + strings.ReplaceAll(strings.TrimSpace(msg[i+1:]), "\n", "\n  ")
		}
		fmt.Fprintf(&b, "%s %s\n", loc, msg)
		if e.Breaks > 2 {
			fmt.Fprintf(&b, "  (same error in %d packages)\n", e.Breaks)
		}
	}
	if n, ok := c["errors_omitted"].(int); ok && n > 0 {
		fmt.Fprintf(&b, "... %d more errors (fix these first, or --verbose)\n", n)
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
