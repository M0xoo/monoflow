package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/graph"

	"github.com/antigravity/monoflow/pkg/improve"
)

// Every agent-run command is journaled so `monoflow improve` can show the agent what its
// session did with MonoFlow (failures, redirects, slow calls) before it files a retro.
var journal struct {
	on    bool
	start time.Time
	root  string
	cmd   string
}

// startJournal records this invocation unless MonoFlow started it itself (MONOFLOW_INTERNAL
// is inherited by every child), it is a hook, help, or the improve command itself.
func startJournal(internal bool) {
	if internal || len(os.Args) < 2 || os.Getenv("MONOFLOW_NO_JOURNAL") == "1" || hasHelpArg(os.Args[2:]) {
		return
	}
	switch os.Args[1] {
	case "hook", "improve", "retro", "conflict-watch", "baseline-warm", "speculate-watch", "help", "--help", "-h", "version-info":
		return
	}
	root, err := findRepoRoot()
	if err != nil {
		return
	}
	journal.on, journal.start, journal.root = true, time.Now(), root
	journal.cmd = strings.Join(os.Args[1:], " ")
}

// exit ends the process, journaling the exit code first. Use it instead of os.Exit.
func exit(code int) {
	finishJournal(code)
	os.Exit(code)
}

func finishJournal(code int) {
	if !journal.on {
		return
	}
	journal.on = false
	_ = improve.OpenStore(journal.root).Record(improve.Entry{Kind: "cmd", Cmd: journal.cmd, Exit: code,
		Ms: time.Since(journal.start).Milliseconds(), Worktree: journal.root, Branch: currentBranch(journal.root)})
}

// journalRedirect records a raw command a harness hook redirected to MonoFlow.
func journalRedirect(command string) {
	root, err := findRepoRoot()
	if err != nil {
		return
	}
	_ = improve.OpenStore(root).Record(improve.Entry{Kind: "redirect", Cmd: strings.TrimSpace(command), Worktree: root})
}

func improveAgent(root string) string {
	if a := os.Getenv("MONOFLOW_AGENT"); a != "" {
		return a
	}
	return strings.TrimPrefix(currentBranch(root), "agent/")
}

func handleImprove(rootDir string) {
	sub := "retro"
	args := os.Args[2:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	var pos []string
	flags := map[string]string{}
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			k, v, _ := strings.Cut(strings.TrimPrefix(a, "--"), "=")
			flags[k] = v
			continue
		}
		pos = append(pos, a)
	}
	store := improve.OpenStore(rootDir)
	fail := func(msg string) {
		fmt.Println("error: " + msg)
		exit(1)
	}
	switch sub {
	case "retro":
		sess := store.Current(rootDir)
		fmt.Print(improve.RetroText(sess, store.Triage(time.Time{}).Open))

	case "add":
		title := strings.Join(pos, " ")
		if title == "" {
			title = flags["title"]
		}
		for k := range flags {
			switch k {
			case "title", "area", "kind", "severity", "detail", "evidence", "same-as":
			default:
				fail(fmt.Sprintf("unknown flag --%s (see monoflow help improve)", k))
			}
		}
		detail := flags["detail"]
		if id := flags["same-as"]; id != "" {
			detail = strings.TrimSpace("same-as:" + id + " " + detail)
			if title == "" {
				for _, s := range store.Suggestions() {
					if s.ID == id {
						title, flags["area"], flags["kind"] = s.Title, firstNonEmpty(flags["area"], s.Area), firstNonEmpty(flags["kind"], s.Kind)
					}
				}
			}
		}
		if title == "" {
			fail(`usage: monoflow improve add "<what MonoFlow should do>" [--area=] [--kind=] [--severity=] [--evidence=] [--detail=]`)
		}
		sg, err := store.Add(improve.Suggestion{Title: title, Area: flags["area"], Kind: flags["kind"],
			Severity: flags["severity"], Detail: detail, Evidence: flags["evidence"],
			Repo: improve.MainRoot(rootDir), Worktree: rootDir, Branch: currentBranch(rootDir), Agent: improveAgent(rootDir)})
		if err != nil {
			fail(err.Error())
		}
		if jsonMode() {
			printOut(sg)
			return
		}
		fmt.Printf("filed %s: %s\nnext: file the next one, or close the retro: monoflow improve done\n", sg.ID, sg.Title)

	case "done", "none":
		filed := store.Current(rootDir).Filed
		r, err := store.Close(rootDir, currentBranch(rootDir), improveAgent(rootDir), flags["note"], sub == "none")
		if err != nil {
			fail(err.Error())
		}
		if sub == "none" && r.Suggestions > 0 {
			fmt.Printf("retro closed with the %d suggestion(s) you filed. Thanks.\n", r.Suggestions)
		} else {
			fmt.Printf("retro closed: %d suggestion(s). Thanks.\n", r.Suggestions)
		}
		// Triage what this session filed (and retry what could not be filed before).
		only := map[string]bool{}
		for _, sg := range filed {
			only[sg.ID] = true
		}
		for id, p := range store.Publications() {
			if !p.Done() {
				only[id] = true
			}
		}
		if len(only) > 0 {
			printOutcomes(publishSuggestions(rootDir, store, only, false))
		}

	case "publish":
		_, dry := flags["dry-run"]
		var only map[string]bool
		if len(pos) > 0 {
			only = map[string]bool{}
			for _, id := range pos {
				only[id] = true
			}
		}
		outs := publishSuggestions(rootDir, store, only, dry)
		if len(outs) == 0 {
			fmt.Println("nothing to triage: every suggestion was handled")
			return
		}
		if dry {
			fmt.Println("dry run: nothing was filed. Would file:")
			for _, o := range outs {
				switch o.Action {
				case "created":
					fmt.Printf("\n--- %s: new issue\n%s\n\n%s", o.Suggestion.ID, o.Title, o.Body)
				case "commented":
					fmt.Printf("\n--- %s: comment on #%d\n%s", o.Suggestion.ID, o.Issue, o.Body)
				default:
					fmt.Printf("\n--- %s: not filed: %s\n", o.Suggestion.ID, o.Reason)
				}
			}
			return
		}
		printOutcomes(outs)

	case "list":
		since := parseSince(flags["since"])
		dec := store.Decisions()
		pubs := store.Publications()
		n := 0
		for _, s := range store.Suggestions() {
			if !s.TS.After(since) {
				continue
			}
			st := "new"
			if d, ok := dec[s.ID]; ok {
				st = d.Status
			}
			if _, all := flags["all"]; !all && !improve.Open(st) {
				continue
			}
			n++
			issue := ""
			if p := pubs[s.ID]; p.URL != "" {
				issue = " · " + p.URL
			}
			fmt.Printf("%s [%s] %s · %s · %s · %s%s\n", s.ID, st, s.Title, firstNonEmpty(s.Area, "-"), firstNonEmpty(s.Kind, "-"), firstNonEmpty(s.Agent, "-"), issue)
		}
		if n == 0 {
			fmt.Println("no suggestions")
		}

	case "triage":
		rep := store.Triage(parseSince(flags["since"]))
		if jsonMode() {
			data, _ := json.MarshalIndent(rep, "", "  ")
			fmt.Println(string(data))
			return
		}
		text := rep.Text()
		_ = os.MkdirAll(store.Dir, 0755)
		_ = os.WriteFile(store.Dir+"/TRIAGE.md", []byte(text), 0644)
		fmt.Print(text)

	case "mark":
		if len(pos) < 2 {
			fail(`usage: monoflow improve mark <id>... <accepted|fixed|wontfix|duplicate|invalid|new> [--note="..."] [--exact]`)
		}
		_, exact := flags["exact"]
		marked, err := store.Mark(pos[:len(pos)-1], pos[len(pos)-1], flags["note"], exact)
		if err != nil {
			fail(err.Error())
		}
		fmt.Printf("marked %s: %s\n", pos[len(pos)-1], strings.Join(marked, " "))

	case "path":
		fmt.Println(store.Dir)

	default:
		fail("unknown subcommand " + sub + " (see monoflow help improve)")
	}
}

// parseSince accepts a duration ("2h") or a date/time ("2026-10-18", RFC 3339).
func parseSince(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

// retroNudge is the Stop hook's request for a retro on MonoFlow, once per session that did
// MonoFlow work ("" when there is nothing to ask). Off with "improve": {"after_session": false}.
func retroNudge(root string) string {
	if !improveAfterSession(root) {
		return ""
	}
	store := improve.OpenStore(root)
	sess := store.Current(root)
	if !store.ClaimNudge(sess) {
		return ""
	}
	return "Before you finish: a 1-minute retro on MonoFlow helps fix what got in your way.\n\n" +
		improve.RetroText(sess, store.Triage(time.Time{}).Open)
}

type improveConfig struct {
	AfterSession *bool  `json:"after_session"`
	Issues       *bool  `json:"issues"`
	IssuesRepo   string `json:"issues_repo"`
}

func readImproveConfig(root string) improveConfig {
	var cfg struct {
		Improve *improveConfig `json:"improve"`
	}
	if data, err := os.ReadFile(root + "/.agents/monoflow.config.json"); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg.Improve == nil {
		return improveConfig{}
	}
	return *cfg.Improve
}

func improveAfterSession(root string) bool {
	if c := readImproveConfig(root); c.AfterSession != nil {
		return *c.AfterSession
	}
	return true
}

// issueTracker is where valid suggestions are filed: MonoFlow's GitHub repository unless
// "improve": {"issues": false} (or MONOFLOW_IMPROVE_ISSUES=0) turns it off; nil when off.
func issueTracker(root string) improve.Tracker {
	c := readImproveConfig(root)
	if os.Getenv("MONOFLOW_IMPROVE_ISSUES") == "0" || (c.Issues != nil && !*c.Issues) {
		return nil
	}
	repo := firstNonEmpty(os.Getenv("MONOFLOW_IMPROVE_ISSUES_REPO"), c.IssuesRepo, improve.DefaultIssueRepo)
	return improve.GHTracker{Repo: repo}
}

func publishSuggestions(root string, store *improve.Store, only map[string]bool, dry bool) []improve.Outcome {
	tr := issueTracker(root)
	if tr == nil && !dry {
		return nil // issues are off: local triage only
	}
	outs, err := store.Publish(improve.PublishOptions{Tracker: tr, Version: monoflowBuild(), Context: issueContext(root),
		Redact: improve.Redactor(improve.MainRoot(root)), DryRun: dry, Only: only})
	if err != nil {
		fmt.Println("triage: could not save results: " + err.Error())
	}
	return outs
}

func printOutcomes(outs []improve.Outcome) {
	if len(outs) == 0 {
		return
	}
	fmt.Println("triage:")
	for _, o := range outs {
		id := o.Suggestion.ID
		switch o.Action {
		case "created":
			fmt.Printf("  %s → issue #%d opened in MonoFlow's repo: %s\n", id, o.Issue, firstNonEmpty(o.URL, "(dry run)"))
		case "commented":
			fmt.Printf("  %s → same problem as issue #%d: your evidence was added there %s\n", id, o.Issue, o.URL)
		case "rejected":
			fmt.Printf("  %s → not filed: %s\n       (to file it properly: monoflow improve add \"<title>\" --same-as=%s --evidence=\"<command + output>\", then improve done)\n", id, o.Reason, id)
		case "pending":
			fmt.Printf("  %s → queued, retried at the next retro (%s)\n", id, o.Reason)
		}
	}
}

// monoflowBuild names the running MonoFlow build (module version or VCS revision).
func monoflowBuild() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	rev, dirty := "", false
	for _, st := range bi.Settings {
		switch st.Key {
		case "vcs.revision":
			rev = st.Value
		case "vcs.modified":
			dirty = st.Value == "true"
		}
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if rev == "" {
		return firstNonEmpty(bi.Main.Version, "unknown")
	}
	if dirty {
		rev += "+dirty"
	}
	return rev
}

// issueContext is what an issue says about where the problem was seen (no names or paths).
func issueContext(root string) string {
	langs := map[string]bool{}
	if g, err := graph.LoadOrDiscover(root); err == nil {
		for _, p := range g.GetAllPackages() {
			if p.Language != "" {
				langs[string(p.Language)] = true
			}
		}
	}
	var ls []string
	for l := range langs {
		ls = append(ls, l)
	}
	sort.Strings(ls)
	ctx := runtime.GOOS + "/" + runtime.GOARCH
	if len(ls) > 0 {
		ctx = strings.Join(ls, ", ") + " repo · " + ctx
	}
	if os.Getenv("CLAUDECODE") == "1" {
		ctx += " · Claude Code"
	}
	return ctx
}

// lastStep is the "you are done" next step: the retro (unless turned off), then end the turn.
func lastStep(root, msg string) string {
	if !improveAfterSession(root) {
		return msg + " End your turn."
	}
	// The retro comes first: after "Shipped." agents went straight to their final report.
	return "run monoflow improve now (1-minute retro on MonoFlow, before your final answer), then end your turn. " + msg
}
