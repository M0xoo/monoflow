package improve

import (
	"fmt"
	"strings"
	"time"
)

// slowCall is how long a single MonoFlow call can take before the retro points it out.
const slowCall = 60 * time.Second

// RetroText is what `monoflow improve` prints: what the session did with MonoFlow, then how
// to file what MonoFlow should do better. known lists open suggestions others already filed.
func RetroText(sess Session, known []ClusterOf) string {
	var b strings.Builder
	cmds := sess.Commands()
	span := ""
	if len(sess.Entries) > 1 {
		if d := sess.Entries[len(sess.Entries)-1].TS.Sub(sess.Entries[0].TS).Round(time.Minute); d > 0 {
			span = " in " + strings.TrimSuffix(d.String(), "0s")
		}
	}
	fmt.Fprintf(&b, "MonoFlow retro (session %s: %d monoflow commands%s)\n", sess.ID, cmds, span)

	counts := map[string]int{}
	fails := map[string]int{}
	var order []string
	var failed, slow, redirected []string
	for _, e := range sess.Entries {
		if e.Kind == "redirect" {
			redirected = append(redirected, oneLine(e.Cmd, 100))
			continue
		}
		name := strings.Fields(e.Cmd + " ?")[0]
		if counts[name] == 0 {
			order = append(order, name)
		}
		counts[name]++
		if e.Exit != 0 {
			fails[name]++
			failed = append(failed, fmt.Sprintf("%s (exit %d)", oneLine(e.Cmd, 100), e.Exit))
		}
		if time.Duration(e.Ms)*time.Millisecond > slowCall {
			slow = append(slow, fmt.Sprintf("%s %.0fs", oneLine(e.Cmd, 60), float64(e.Ms)/1000))
		}
	}
	if len(order) > 0 {
		var parts []string
		for _, n := range order {
			p := fmt.Sprintf("%s %d", n, counts[n])
			if fails[n] > 0 {
				p += fmt.Sprintf(" (%d failed)", fails[n])
			}
			parts = append(parts, p)
		}
		b.WriteString("  ran: " + strings.Join(parts, " · ") + "\n")
	}
	list := func(label string, items []string) {
		if len(items) == 0 {
			return
		}
		items = dedupe(items)
		if len(items) > 6 {
			items = append(items[:6], fmt.Sprintf("… %d more", len(items)-6))
		}
		b.WriteString("  " + label + ": " + strings.Join(items, "; ") + "\n")
	}
	list("non-zero exits", failed)
	list("raw commands MonoFlow redirected", redirected)
	list("slow", slow)
	if len(sess.Filed) > 0 {
		var t []string
		for _, s := range sess.Filed {
			t = append(t, s.ID+" "+oneLine(s.Title, 80))
		}
		list("filed so far", t)
	}

	b.WriteString(`
Retro on MonoFlow itself (not on your task's code): what failed, misled you, cost extra calls
or tokens, made you guess, or forced a raw tool or a workaround? A non-zero exit from verify on
a real bug in your code is MonoFlow working; a wrong, unclear or slow answer is not.
File each problem (one call each; the title says what MonoFlow should do):
  monoflow improve add "<what MonoFlow should do>" --area=<verify|ship|start|find|graph|events|docs|...> \
    --kind=bug|friction|missing|docs --severity=high|medium|low \
    --evidence="<exact command + output snippet>" [--detail="<why, what you did instead>"]
  (--area: the top-level monoflow command involved, or docs for AGENTS.md / help text; all flags optional)
`)
	if len(known) > 0 {
		b.WriteString("Already reported by other sessions (if it is one of these: improve add --same-as=<id> --evidence=\"...\"):\n")
		n := 0
		for _, c := range known {
			if n == 5 {
				break
			}
			l := c.Lead()
			fmt.Fprintf(&b, "  %s %s\n", l.ID, oneLine(l.Title, 100))
			n++
		}
	}
	b.WriteString("Then close the retro: monoflow improve done   (nothing worth improving: monoflow improve none)\n")
	return b.String()
}

func dedupe(items []string) []string {
	seen := map[string]int{}
	var out []string
	for _, it := range items {
		if _, ok := seen[it]; !ok {
			out = append(out, it)
		}
		seen[it]++
	}
	for i, it := range out {
		if n := seen[it]; n > 1 {
			out[i] = fmt.Sprintf("%s ×%d", it, n)
		}
	}
	return out
}
