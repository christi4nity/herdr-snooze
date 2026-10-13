package main

import (
	"errors"
	"time"
)

// demoWorld is the popup on made-up agents: to try it, or to take
// screenshots without showing real work. Snoozing and waking change only
// what it shows.
type demoWorld struct {
	st     *state
	panes  []pane
	spaces []workspaceInfo
}

func newDemoWorld() *demoWorld {
	spaces := []workspaceInfo{
		{"w1", "payments-api"}, {"w2", "web"}, {"w3", "infra"}, {"w4", "docs"},
	}
	agent := func(id, ws, agent, status, title string) pane {
		return pane{PaneID: id, WorkspaceID: ws, TerminalID: "t-" + id, Agent: agent, AgentStatus: status, TitleStripped: title}
	}
	panes := []pane{
		agent("w1:p1", "w1", "claude", "blocked", "ENG-142 Retry failed webhooks with backoff"),
		withPR(agent("w1:p2", "w1", "codex", "working", "Idempotency keys on POST /charges"), "#482"),
		agent("w2:p1", "w2", "claude", "done", "ENG-156 Dark mode for the settings page"),
		agent("w2:p2", "w2", "claude", "idle", "Flaky checkout e2e test"),
		agent("w3:p1", "w3", "claude", "idle", "Terraform: move the queue to its own VPC"),
		agent("w3:p2", "w3", "opencode", "done", "Bump the base image to Debian 13"),
		agent("w4:p1", "w4", "claude", "idle", "Rewrite the onboarding guide"),
		agent("w4:p2", "w4", "codex", "working", "API reference from the OpenAPI spec"),
	}
	// One more, snoozed till it's done.
	panes = append(panes, agent("w3:p3", "w3", "claude", "working", "Load test the new queue"))
	now := timeNow()
	at := func(d time.Duration) int64 { return now.Add(d).UnixMilli() }
	tomorrow9 := time.Date(now.Year(), now.Month(), now.Day()+1, 9, 0, 0, 0, now.Location()).UnixMilli()
	st := emptyState()
	st.Workspaces["w4"] = &spaceEntry{Until: at(3*24*time.Hour + 2*time.Hour)}
	st.Panes["w4:p1"] = &paneEntry{Until: st.Workspaces["w4"].Until, Via: viaWorkspace, WorkspaceID: "w4"}
	st.Panes["w4:p2"] = &paneEntry{Until: st.Workspaces["w4"].Until, Via: viaWorkspace, WorkspaceID: "w4"}
	st.Panes["w3:p1"] = &paneEntry{Until: at(2*time.Hour + 14*time.Minute), Via: viaPane, WorkspaceID: "w3"}
	st.Panes["w2:p2"] = &paneEntry{Until: tomorrow9, Via: viaPane, WorkspaceID: "w2"}
	st.Panes["w3:p3"] = &paneEntry{Until: conditionCap(now).UnixMilli(), Via: viaPane, WorkspaceID: "w3", When: whenDone}
	return &demoWorld{st: st, panes: panes, spaces: spaces}
}

func (d *demoWorld) copyState() *state {
	st := emptyState()
	for k, v := range d.st.Panes {
		e := *v
		st.Panes[k] = &e
	}
	for k, v := range d.st.Workspaces {
		e := *v
		st.Workspaces[k] = &e
	}
	return st
}

func (d *demoWorld) load() loadedMsg {
	return loadedMsg{st: d.copyState(), panes: d.panes, spaces: d.spaces}
}

func (d *demoWorld) snooze(t target, until time.Time, when string, _ config) error {
	ms := until.UnixMilli()
	if t.Kind == "workspace" {
		d.st.Workspaces[t.ID] = &spaceEntry{Until: ms}
		for _, p := range d.panes {
			if p.WorkspaceID == t.ID && p.Agent != "" && d.st.Panes[p.PaneID] == nil {
				d.st.Panes[p.PaneID] = &paneEntry{Until: ms, Via: viaWorkspace, WorkspaceID: t.ID}
			}
		}
		return nil
	}
	ws := ""
	for _, p := range d.panes {
		if p.PaneID == t.ID {
			ws = p.WorkspaceID
		}
	}
	d.st.Panes[t.ID] = &paneEntry{Until: ms, Via: viaPane, WorkspaceID: ws, When: when}
	return nil
}

func (d *demoWorld) wake(_ config, panes, spaces []string, all bool) error {
	for wid := range d.st.Workspaces {
		if all || contains(spaces, wid) {
			delete(d.st.Workspaces, wid)
			for pid, e := range d.st.Panes {
				if e.Via == viaWorkspace && e.WorkspaceID == wid {
					delete(d.st.Panes, pid)
				}
			}
		}
	}
	for pid := range d.st.Panes {
		if all || contains(panes, pid) {
			delete(d.st.Panes, pid)
		}
	}
	return nil
}

// withPR gives a demo agent an open pull request, as herdr-github tags one.
func withPR(p pane, pr string) pane {
	p.Tokens = map[string]any{"pr": pr, "pr_state": "open"}
	return p
}

var errDemo = errors.New("that's for real agents: this is the demo")

func (d *demoWorld) focus(string) error  { return errDemo }
func (d *demoWorld) open(popupRef) error { return errDemo }
func (d *demoWorld) siblings() map[string]popupRef {
	return map[string]popupRef{recapPlugin: {PluginID: recapPlugin, Entrypoint: "picker"}}
}
