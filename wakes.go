package main

import (
	"fmt"
	"strings"
)

// woken is an agent that woke by itself: its time came, or what it was
// snoozed until happened. A wake you asked for (the wake key, the list,
// Recap) isn't one: you know already.
type woken struct {
	PaneID      string
	Name        string
	Agent       string
	WorkspaceID string
	// Reason is what happened ("it's done"); "" for time up.
	Reason string
	// Space: it woke with its snoozed space.
	Space bool
}

func wokenFrom(p pane, e *paneEntry, reason string) woken {
	return woken{PaneID: p.PaneID, Name: paneLabel(p), Agent: p.Agent, WorkspaceID: p.WorkspaceID,
		Reason: reason, Space: e.Via == viaWorkspace}
}

// announceWakes is one toast for whatever woke in a tick: herdr shows one
// plugin toast a second at most, and a space waking is one event, not one per
// agent. It names the agent, so "awake" says which.
func announceWakes(woke []woken, spacesWoke []string) {
	if len(woke) == 0 || !loadConfig().NotifyWake {
		return
	}
	labels := map[string]string{}
	if spaces, err := listWorkspaces(); err == nil {
		for _, w := range spaces {
			labels[w.WorkspaceID] = w.Label
		}
	}
	title, body := wakeToast(woke, spacesWoke, labels)
	notify(title, body)
	logf("announced: %s", title)
}

func wakeToast(woke []woken, spacesWoke []string, labels map[string]string) (title, body string) {
	capital := func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToUpper(s[:1]) + s[1:]
	}
	if len(woke) == 1 {
		w := woke[0]
		var parts []string
		for _, s := range []string{capital(w.Reason), w.Agent, labels[w.WorkspaceID]} {
			if s != "" {
				parts = append(parts, s)
			}
		}
		return w.Name + " is awake", strings.Join(parts, " · ")
	}
	var names []string
	for _, w := range woke {
		name := w.Name
		if w.Reason != "" {
			name += " (" + w.Reason + ")"
		}
		names = append(names, name)
	}
	body = strings.Join(names, ", ")
	if len(spacesWoke) == 1 {
		wid := spacesWoke[0]
		all := true
		for _, w := range woke {
			all = all && w.Space && w.WorkspaceID == wid
		}
		if label := labels[wid]; all && label != "" {
			return label + " is awake", fmt.Sprintf("%d agents: %s", len(woke), body)
		}
	}
	return fmt.Sprintf("%d agents are awake", len(woke)), body
}

// The "Just woke" list lasts this long at most: an agent you haven't gone
// to by then has had its chance.
const justWokeMS = 4 * 3600 * 1000

// Tokens on a pane on the "Just woke" list. The badge alone is for showing:
// in `snoozed`, where "$snoozed" already shows a snoozed agent's 💤 (so it's
// there with no change to herdr's config), and in wokeBadge, for "$woke" on
// its own (next to state_icon, it reads as the agent's icon). wokeToken is
// for other plugins (Recap): when, and why ("14:05 · it's done").
const (
	wokeBadge = "woke"
	wokeToken = "snooze_woke"
)

const defaultWokeBadge = "⏰"

// wokeText is wokeToken's value: when it woke, and why if not time.
func wokeText(e *wokeEntry, now int64) string {
	text := formatUntil(fromMS(e.At), fromMS(now))
	if e.Reason != "" {
		text += " · " + e.Reason
	}
	return text
}

// trackWoke keeps the "Just woke" list: what woke by itself in this run goes
// on it, and an agent leaves it once you go to it (focus on it, after it's
// been seen without), snooze it again, it
// closes or another agent starts in its pane, or justWokeMS has passed. It
// returns the token ops to send. Its tokens die by their own TTL too, so a
// lost state file leaves none for long.
func trackWoke(st *state, panes []pane, now int64, force bool, badge string) []tokenOp {
	live := map[string]pane{}
	for _, p := range panes {
		live[p.PaneID] = p
	}
	for _, w := range st.Woken {
		if p, ok := live[w.PaneID]; ok {
			st.Woke[w.PaneID] = &wokeEntry{At: now, Reason: w.Reason, AgentID: agentIdentity(p), Away: !p.Focused}
		}
	}
	var ops []tokenOp
	for _, pid := range sortedKeys(st.Woke) {
		e := st.Woke[pid]
		p, ok := live[pid]
		if !ok {
			delete(st.Woke, pid)
			continue
		}
		_, shown := p.Tokens[wokeToken]
		if p.Focused && e.Away || st.Panes[pid] != nil || now >= e.At+justWokeMS || replaced(&paneEntry{AgentID: e.AgentID}, p) {
			delete(st.Woke, pid)
			if _, badged := p.Tokens[wokeBadge]; shown || badged {
				op := tokenOp{Kind: opWokeClear, Target: pid}
				if st.Panes[pid] != nil {
					op.Badge = "keep" // snoozed again: `snoozed` is its 💤 now
				}
				ops = append(ops, op)
			}
			continue
		}
		e.Away = e.Away || !p.Focused
		if force || !shown {
			ops = append(ops, tokenOp{Kind: opWokeReport, Target: pid, Value: wokeText(e, now), Badge: badge,
				TTL: e.At + justWokeMS - now})
		}
	}
	return ops
}
