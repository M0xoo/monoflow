package verify

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SharedError is one distinct failure and the targets that report it.
type SharedError struct {
	DiagnosticError
	Breaks  int      `json:"breaks_targets"`     // how many targets report this error
	Targets []string `json:"targets,omitempty"` // named only when there are at most 2
}

// DedupeErrors moves per-target errors into res.Errors, one entry per distinct failure.
// A broken upstream file is reported by every dependent that compiles it (22 copies of the same
// line in an opentelemetry-go rename); the agent needs it once, with who it breaks. Paths become
// repo-relative so the same file reads the same from every target. pathOf maps target -> package
// dir (repo-relative). At most max entries are kept, most widespread first.
func DedupeErrors(res *SuiteResult, root string, pathOf map[string]string, max int) {
	index := map[string]int{}
	var all []SharedError
	for i := range res.Results {
		r := &res.Results[i]
		if len(r.Errors) == 0 {
			continue
		}
		for _, e := range r.Errors {
			e.File = repoRelative(root, pathOf[r.Target], e.File)
			if len(e.Message) > 400 {
				e.Message = e.Message[:400] + "…"
			}
			key := e.File + ":" + strconv.Itoa(e.Line) + ":" + strconv.Itoa(e.Col) + ":" + e.Message
			if j, ok := index[key]; ok {
				if t := all[j].Targets; t[len(t)-1] != r.Target {
					all[j].Targets = append(t, r.Target)
				}
				continue
			}
			index[key] = len(all)
			all = append(all, SharedError{DiagnosticError: e, Targets: []string{r.Target}})
		}
		r.ErrorCount = len(r.Errors)
		r.Errors = nil
	}
	sort.SliceStable(all, func(a, b int) bool { return len(all[a].Targets) > len(all[b].Targets) })
	if max > 0 && len(all) > max {
		res.ErrorsOmitted = len(all) - max
		all = all[:max]
	}
	for i := range all {
		all[i].Breaks = len(all[i].Targets)
		if all[i].Breaks > 2 {
			all[i].Targets = nil
		}
	}
	res.Errors = all
}

func repoRelative(root, pkgDir, file string) string {
	if file == "" {
		return file
	}
	abs := file
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, pkgDir, file)
	}
	if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return file
}
