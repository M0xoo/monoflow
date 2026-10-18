package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

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
		r, err := store.Close(rootDir, currentBranch(rootDir), improveAgent(rootDir), flags["note"], sub == "none")
		if err != nil {
			fail(err.Error())
		}
		if sub == "none" && r.Suggestions > 0 {
			fmt.Printf("retro closed with the %d suggestion(s) you filed. Thanks.\n", r.Suggestions)
			return
		}
		fmt.Printf("retro closed: %d suggestion(s). Thanks.\n", r.Suggestions)

	case "list":
		since := parseSince(flags["since"])
		dec := store.Decisions()
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
			fmt.Printf("%s [%s] %s · %s · %s · %s\n", s.ID, st, s.Title, firstNonEmpty(s.Area, "-"), firstNonEmpty(s.Kind, "-"), firstNonEmpty(s.Agent, "-"))
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

func improveAfterSession(root string) bool {
	data, err := os.ReadFile(root + "/.agents/monoflow.config.json")
	if err != nil {
		return true
	}
	var cfg struct {
		Improve *struct {
			AfterSession *bool `json:"after_session"`
		} `json:"improve"`
	}
	if json.Unmarshal(data, &cfg) == nil && cfg.Improve != nil && cfg.Improve.AfterSession != nil {
		return *cfg.Improve.AfterSession
	}
	return true
}

// lastStep is the "you are done" next step: the retro (unless turned off), then end the turn.
func lastStep(root, msg string) string {
	if !improveAfterSession(root) {
		return msg + " End your turn."
	}
	// The retro comes first: after "Shipped." agents went straight to their final report.
	return "run monoflow improve now (1-minute retro on MonoFlow, before your final answer), then end your turn. " + msg
}
