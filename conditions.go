package main

import "time"

// A snooze can be until something happens, not only until a time: the
// agent finishing what it's doing, or its pull request being merged. These
// are short waits, so each is capped at the next morning (what "tomorrow"
// means), and the agent comes back then if it hasn't happened.
const (
	whenDone   = "done"   // it stops working: done, idle, or stuck
	whenMerged = "merged" // its pull request (herdr-github's tokens) is merged or closed
)

// conditionCap is the latest a conditional snooze lasts: tomorrow 9:00.
func conditionCap(now time.Time) time.Time {
	until, _ := parseUntil("tomorrow", now)
	return until
}

// conditionLabel is what the badge and the list say for one: "till done".
func conditionLabel(when string) string { return "till " + when }

// canWaitFor is whether a snooze until when makes sense for the agent in p
// now: until done while it's working, until merged while it has a pull
// request open.
func canWaitFor(when string, p pane) bool {
	switch when {
	case whenDone:
		return p.AgentStatus == "working"
	case whenMerged:
		pr, _ := p.Tokens["pr"].(string)
		state, _ := p.Tokens["pr_state"].(string)
		return pr != "" && (state == "open" || state == "draft")
	}
	return false
}

// prSettled is whether the pane's pull request is merged, or closed (then it
// never will be).
func prSettled(p pane) bool {
	state, _ := p.Tokens["pr_state"].(string)
	return state == "merged" || state == "closed"
}

// wakeOnChange ends the snoozes whose moment has come, from what the panes
// say now and what they said last time: a snoozed agent that comes to need
// you (when wakeBlocked), one snoozed until done that stops working, one
// snoozed until merged whose pull request is. The record goes, and
// reconcile then clears the token it finds with no record behind it.
//
// Only a change counts for blocked: an agent snoozed while it was already
// waiting on you was snoozed knowing that, and stays snoozed until it moves
// on and gets stuck again. Statuses come from the hook that just fired, or
// at worst from the next tick: herdr has no event for another plugin's
// token changing, so a merge is noticed within the five minutes of the
// safety tick.
func wakeOnChange(st *state, panes []pane, wakeBlocked bool) {
	live := map[string]pane{}
	for _, p := range panes {
		live[p.PaneID] = p
	}
	for _, pid := range sortedKeys(st.Panes) {
		e := st.Panes[pid]
		p, ok := live[pid]
		if !ok || p.Agent == "" {
			continue // closed, or no agent: reconcile's to deal with
		}
		was, now := e.Status, p.AgentStatus
		e.Status = now
		reason := ""
		switch {
		case wakeBlocked && now == "blocked" && was != "" && was != "blocked":
			reason = "it needs you"
		case e.When == whenDone && was == "working" && (now == "done" || now == "idle" || now == "blocked"):
			reason = "it's done"
		case e.When == whenMerged && prSettled(p):
			reason = "its pull request is " + p.Tokens["pr_state"].(string)
		}
		if reason == "" {
			continue
		}
		delete(st.Panes, pid)
		if space := st.Workspaces[e.WorkspaceID]; space != nil {
			space.addExcept(pid) // out of a still-snoozed space: don't re-adopt it
		}
		st.Woken = append(st.Woken, wokenFrom(p, e, reason))
		logf("woke %s: %s", pid, reason)
	}
}
