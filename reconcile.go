package main

import (
	"encoding/json"
	"sort"
	"strings"
)

// Token names aren't owned in herdr: any plugin could write `snoozed`. So
// every snoozed pane carries two: `snoozed`, the badge text people show in
// their sidebar ("$snoozed"), and mark, which is snooze's alone. Filters and
// "is this ours?" go by mark; `snoozed` is only for display.
const (
	token = "snoozed"
	mark  = "herdr_snooze"
)

const (
	maxTTLMS        = 86400000         // herdr's cap on a metadata token's ttl_ms
	refreshMarginMS = 12 * 3600 * 1000 // re-report long snoozes this far before the token dies
	detectedGraceMS = 10 * 1000        // see agentDetected
)

// Where a pane's snooze comes from (paneEntry.Via).
const (
	viaPane      = "pane"
	viaWorkspace = "workspace"
)

// tokenOp kinds.
const (
	opReport   = "report"
	opClear    = "clear"
	opWsReport = "ws_report"
	opWsClear  = "ws_clear"
)

// tokenOp is a token change for herdr: report (with a value and a TTL) or
// clear, on a pane or (ws_) a workspace.
type tokenOp struct {
	Kind   string
	Target string
	Value  string
	TTL    int64
}

func tokenDue(dirty *bool, tokenUntil, until, now int64, present bool) bool {
	was := *dirty
	*dirty = false
	if was || !present {
		return true
	}
	return tokenUntil < until && tokenUntil-now < refreshMarginMS
}

func tokenOpFor(kind, target string, until int64, tokenUntil *int64, now int64, badge string) tokenOp {
	ttl := max(1, min(until-now, maxTTLMS))
	*tokenUntil = now + ttl
	return tokenOp{kind, target, badged(badge, formatUntil(fromMS(until), fromMS(now))), ttl}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// reconcile brings st in line with the live panes and the clock, and
// returns the token ops to send.
func reconcile(st *state, panes []pane, now int64, force bool, badge string) []tokenOp {
	var ops []tokenOp
	live := map[string]pane{}
	liveSpaces := map[string]bool{}
	for _, p := range panes {
		live[p.PaneID] = p
		liveSpaces[p.WorkspaceID] = true
	}
	followTerminals(st, panes, live)

	for _, wid := range sortedKeys(st.Workspaces) {
		space := st.Workspaces[wid]
		if !liveSpaces[wid] || now >= space.Until {
			// No panes left in it (if the space itself is gone the clear
			// just fails quietly; if it is merely empty, this takes its badge
			// off), or its time is up.
			ops = append(ops, tokenOp{Kind: opWsClear, Target: wid})
			delete(st.Workspaces, wid)
			continue
		}
		// Agents in the space join it — including ones that start later (the
		// pane.agent_detected hook lands here) — unless that pane was
		// deliberately woken, or given its own deadline. Shells are left
		// alone: a token on one hides nothing and makes every count lie.
		for _, p := range panes {
			if p.Agent == "" || p.WorkspaceID != wid {
				continue
			}
			if _, held := st.Panes[p.PaneID]; held || space.excepts(p.PaneID) {
				continue
			}
			st.Panes[p.PaneID] = &paneEntry{
				Until: space.Until, Via: viaWorkspace, WorkspaceID: wid, Dirty: true,
				AgentID: agentIdentity(p), TerminalID: p.TerminalID, Since: now,
			}
		}
		// Workspace tokens are not visible from pane.list, so only the clock
		// (or a forced pass after a server restart) can say one needs sending.
		if tokenDue(&space.Dirty, space.TokenUntil, space.Until, now, !force) {
			ops = append(ops, tokenOpFor(opWsReport, wid, space.Until, &space.TokenUntil, now, badge))
		}
	}

	for _, pid := range sortedKeys(st.Panes) {
		entry := st.Panes[pid]
		p, ok := live[pid]
		if !ok {
			delete(st.Panes, pid)
			continue
		}
		if now >= entry.Until {
			// The TTL normally got here first; this covers a late token
			// (herdr's clock pauses while the machine sleeps) and makes
			// wake-ups explicit.
			ops = append(ops, tokenOp{Kind: opClear, Target: pid})
			delete(st.Panes, pid)
			continue
		}
		if replaced(entry, p) {
			if space := st.Workspaces[entry.WorkspaceID]; entry.Via == viaWorkspace && space != nil {
				// A snoozed space covers whatever agent runs in it: keep it
				// hidden, as this agent's own record.
				entry.AgentID = agentIdentity(p)
				entry.AgentGone = false
			} else {
				// The agent that was snoozed exited and another started in
				// the same pane: the snooze was for that one, not this.
				ops = append(ops, tokenOp{Kind: opClear, Target: pid})
				delete(st.Panes, pid)
				continue
			}
		}
		_, present := p.Tokens[mark]
		if tokenDue(&entry.Dirty, entry.TokenUntil, entry.Until, now, present && !force) {
			ops = append(ops, tokenOpFor(opReport, pid, entry.Until, &entry.TokenUntil, now, badge))
		}
	}

	// A token with no record behind it (state file lost, a run that died
	// halfway) is an agent hidden for up to a day that no list shows. mark is
	// ours alone; a `snoozed` token without it is taken for ours only if 0.1.x
	// could have written it (it starts with our badge): another plugin's
	// stays.
	cleared := map[string]bool{}
	for _, op := range ops {
		if op.Kind == opClear {
			cleared[op.Target] = true
		}
	}
	for _, p := range panes {
		_, marked := p.Tokens[mark]
		value, named := p.Tokens[token]
		ours := marked || (named && looksOurs(value, badge))
		if _, held := st.Panes[p.PaneID]; !held && !cleared[p.PaneID] && ours {
			ops = append(ops, tokenOp{Kind: opClear, Target: p.PaneID})
		}
	}
	return ops
}

// looksOurs is whether a `snoozed` token without mark is one snooze 0.1.x
// wrote: it always started with the badge. With no badge configured there's
// nothing to tell by, so it's taken for ours.
func looksOurs(value any, badge string) bool {
	s, ok := value.(string)
	if !ok {
		b, _ := json.Marshal(value)
		s = string(b)
	}
	return badge == "" || strings.HasPrefix(s, badge)
}

// followTerminals: herdr gives a pane moved to another space or tab a new
// ID, but it keeps its terminal. What we hold for it is re-keyed by
// terminal_id, so the snooze (or a wake-up exception) follows the pane from
// whatever pane.list shows, whatever order concurrent hooks run in, and
// however many moves ago. Everything learns its terminal while its pane is
// live.
func followTerminals(st *state, panes []pane, live map[string]pane) {
	byTerminal := map[string]pane{}
	for _, p := range panes {
		if p.TerminalID != "" {
			byTerminal[p.TerminalID] = p
		}
	}
	for _, pid := range sortedKeys(st.Panes) {
		entry := st.Panes[pid]
		if p, ok := live[pid]; ok {
			if p.TerminalID != "" {
				entry.TerminalID = p.TerminalID
			}
			continue
		}
		p, ok := byTerminal[entry.TerminalID]
		if entry.TerminalID == "" || !ok {
			continue // closed: reconcile forgets it
		}
		delete(st.Panes, pid)
		if _, has := st.Panes[p.PaneID]; has {
			continue // it already has a record of its own
		}
		if entry.Via == viaWorkspace && p.WorkspaceID != entry.WorkspaceID {
			entry.Via = viaPane // it left the snoozed space: it keeps its own deadline
		}
		entry.WorkspaceID = p.WorkspaceID
		entry.Dirty = true // the token may not have come along
		st.Panes[p.PaneID] = entry
	}
	for _, wid := range sortedKeys(st.Workspaces) {
		space := st.Workspaces[wid]
		if len(space.Except) == 0 {
			continue
		}
		var kept []string
		terminals := map[string]string{}
		for _, pid := range space.Except {
			p, ok := live[pid]
			if !ok {
				if t := space.ExceptTerminals[pid]; t != "" {
					p, ok = byTerminal[t]
				}
			}
			if !ok {
				continue // closed: nothing left to except
			}
			if !contains(kept, p.PaneID) {
				kept = append(kept, p.PaneID)
			}
			if p.TerminalID != "" {
				terminals[p.PaneID] = p.TerminalID
			}
		}
		space.Except, space.ExceptTerminals = kept, terminals
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// replaced is whether the agent in the pane is not the one that was
// snoozed: another kind, or any agent after this pane was seen with none, or
// after herdr reported the snoozed one leaving or another arriving
// (agentDetected).
//
// pane.list carries no process identity, and agent_session changes on the
// same agent's /clear, resume and compact, so neither can tell. What herdr
// does give: pane.agent_detected when an agent's process exits (a release,
// so a quick restart of the same agent is seen too; not on /clear and the
// like); and `agent` cleared when the agent exits, or after several missed
// probes while something else holds the foreground, so an agent that hands
// its terminal to an editor for a while loses its snooze and shows again. A
// record from before 0.2 (no identity) adopts the agent it finds.
func replaced(entry *paneEntry, p pane) bool {
	current := agentIdentity(p)
	if current == nil {
		entry.AgentGone = true // it hides nothing until an agent returns
		return false
	}
	if entry.AgentGone {
		return true
	}
	if entry.AgentID == nil {
		entry.AgentID = current
		return false
	}
	return entry.AgentID.Agent != current.Agent
}

// detectedEvent is herdr's pane.agent_detected: sent when a pane's agent
// comes, goes or changes kind, and when an agent is released (for Claude,
// Codex and the like, that is its process exiting), with Released set and
// Agent still naming the one that left.
type detectedEvent struct {
	PaneID   string
	Agent    string
	Released bool
}

// findDetected is the part of a hook's event JSON that names the pane (with
// its agent and released beside it), wherever herdr nests it.
func findDetected(v any) (detectedEvent, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return detectedEvent{}, false
	}
	if pid, ok := m["pane_id"].(string); ok {
		agent, _ := m["agent"].(string)
		released, _ := m["released"].(bool)
		return detectedEvent{pid, agent, released}, true
	}
	for _, k := range sortedKeys(m) {
		if found, ok := findDetected(m[k]); ok {
			return found, true
		}
	}
	return detectedEvent{}, false
}

// agentDetected applies a pane.agent_detected event: if a snooze in that
// pane predates it, the agent it was for is gone, and the next reconcile
// treats the pane as replaced. A release, or the agent leaving, counts at
// once; an agent arriving counts only after detectedGraceMS (sooner, it's
// the snoozed agent's own detection, delivered late: the picker only offers
// panes whose agent herdr already had). Returns whether a record was marked.
//
// received is when the hook started, not when it got the lock: herdr runs
// hooks concurrently and in no order, so a release of the agent before can
// reach the lock after the one that replaced it was snoozed. An event from
// before the snooze is not about it.
func agentDetected(st *state, ev detectedEvent, received int64) bool {
	entry := st.Panes[ev.PaneID]
	if entry == nil {
		return false
	}
	age := received - entry.Since
	left := ev.Released || ev.Agent == ""
	if age < 0 || (!left && age < detectedGraceMS) {
		return false
	}
	entry.AgentGone = true
	return true
}
