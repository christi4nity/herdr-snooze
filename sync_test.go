package main

import (
	"os"
	"path/filepath"
	"testing"
)

// syncRig is the whole loop as the hooks run it, against a fake herdr whose
// view radar holds: "work" focused and active, "zz" and "zz2" snoozed.
type syncRig struct {
	t     *testing.T
	e     *env
	cfg   config
	state *state
}

func newSyncRig(t *testing.T) *syncRig {
	panes := []map[string]any{fpane("work", map[string]any{"focused": true}), fpane("zz", nil), fpane("zz2", nil)}
	e := newEnv(t, panes, map[string]any{"active": true, "source": radar, "label": "active"})
	far := nowMS() + hourMS
	cfg := defaultConfig()
	cfg.Durations, cfg.Badge = []string{"1h"}, "z"
	return &syncRig{t: t, e: e, cfg: cfg,
		state: stateOf(map[string]*paneEntry{"zz": {Until: far, Dirty: true}, "zz2": {Until: far, Dirty: true}}, nil)}
}

func (r *syncRig) sync() map[string]any {
	r.t.Helper()
	if _, err := syncState(r.state, r.cfg, syncOpts{}); err != nil {
		r.t.Fatal(err)
	}
	return r.e.herdr.lastViewParams()
}

func (r *syncRig) visit() { r.state.View.Showing, r.state.View.FromPane = showingSnoozed, "work" } // what toggle does

func TestToggleVisitAndLeave(t *testing.T) {
	r := newSyncRig(t)
	hidden := r.sync()
	sameJSON(t, []any{hidden["label"], hidden["filter"]}, []any{"active z 2", viewFilter}, "hidden")
	if tk := r.e.herdr.tokens("zz"); tk[mark] != "1" || tk[token] == nil {
		t.Fatal(tk)
	}
	r.visit()
	shown := r.sync()
	sameJSON(t, []any{shown["label"], shown["filter"], shown["sort"]},
		[]any{"z snoozed only · 2", snoozedListFilter, hidden["sort"]}, "shown") // radar's order in both
	sameJSON(t, r.sync()["filter"], snoozedListFilter, "hooks firing while you read it")
	r.e.herdr.focus("zz")
	sameJSON(t, r.sync()["filter"], snoozedListFilter, "looking at a snoozed agent")
	r.e.herdr.focus("work")
	back := r.sync() // walked away: the normal panel returns by itself
	sameJSON(t, []any{back["label"], back["filter"]}, []any{"active z 2", viewFilter}, "back")
	if r.state.View.Showing != "" {
		t.Fatal(r.state.View)
	}
}

func TestAutoReturnCanBeTurnedOff(t *testing.T) {
	r := newSyncRig(t)
	r.cfg.AutoReturn = false
	r.sync()
	r.visit()
	r.e.herdr.focus("zz")
	r.sync()
	r.e.herdr.focus("work")
	sameJSON(t, r.sync()["filter"], snoozedListFilter, "returned")
}

func TestLastSnoozeEndingInTheSnoozedListGivesRadarItsViewBack(t *testing.T) {
	r := newSyncRig(t)
	r.sync()
	r.visit()
	r.sync()
	for _, e := range r.state.Panes {
		e.Until = nowMS() - 1
	}
	params := r.sync()
	sameJSON(t, r.e.herdr.currentView(), map[string]any{"active": true, "source": radar, "label": "active"}, "view")
	if params["filter"] != nil || !r.state.idle() {
		t.Fatal(params, r.state)
	}
	for _, pid := range []string{"work", "zz", "zz2"} {
		if _, ok := r.e.herdr.tokens(pid)[token]; ok {
			t.Fatal(pid, "still badged")
		}
	}
}

func TestUsingRadarsOwnKeyMidVisitLandsYouOnTheNormalPanel(t *testing.T) {
	r := newSyncRig(t)
	r.sync()
	r.visit()
	r.sync()
	r.e.herdr.edit(func() { // the user flipped radar
		r.e.herdr.view = map[string]any{"active": true, "source": radar, "label": "recent"}
	})
	again := r.sync()
	// hiding carries on under radar's new order; the snoozed list is not re-imposed
	sameJSON(t, []any{again["label"], again["filter"], again["sort"]},
		[]any{"recent z 2", viewFilter, knownViews[radar].sorts["recent"]}, "again")
	if r.state.View.Showing != "" {
		t.Fatal(r.state.View)
	}
}

func TestRestartNeverComesBackUpInTheSnoozedList(t *testing.T) {
	r := newSyncRig(t)
	r.sync()
	r.visit()
	r.sync()
	r.e.herdr.edit(func() { // server restarted: the view gone, and the tokens with it
		r.e.herdr.view, r.e.herdr.viewParams = map[string]any{"active": false}, nil
		for _, p := range r.e.herdr.panes {
			p["tokens"] = map[string]any{}
		}
	})
	if _, err := syncState(r.state, r.cfg, syncOpts{force: true}); err != nil {
		t.Fatal(err)
	}
	sameJSON(t, r.e.herdr.lastViewParams()["filter"], viewFilter, "filter")
	if r.state.View.Showing != "" {
		t.Fatal(r.state.View)
	}
	for _, pid := range []string{"zz", "zz2"} {
		if _, ok := r.e.herdr.tokens(pid)[token]; !ok {
			t.Fatal(pid, "not hidden again")
		}
	}
}

func TestShellOrPopupTakingFocusDoesNotEndTheVisit(t *testing.T) {
	r := newSyncRig(t)
	r.e.herdr.edit(func() { r.e.herdr.panes = append(r.e.herdr.panes, fpane("shell", map[string]any{"agent": nil})) })
	r.sync()
	r.visit()
	r.e.herdr.focus("zz")
	r.sync()
	r.e.herdr.focus("shell")
	sameJSON(t, r.sync()["filter"], snoozedListFilter, "filter")
}

func TestBlockedAgentsStayVisibleInTheSnoozedListAndTheLabelSaysSo(t *testing.T) {
	r := newSyncRig(t)
	r.sync()
	r.visit()
	if l := r.sync()["label"]; l != "z snoozed only · 2" {
		t.Fatal(l)
	}
	r.e.herdr.edit(func() { r.e.herdr.pane("work")["agent_status"] = "blocked" }) // the active one now needs you
	shown := r.sync()
	if shown["label"] != "z snoozed only · 2 + 1 blocked" {
		t.Fatal(shown["label"])
	}
	sameJSON(t, shown["filter"], snoozedListFilter, "the filter lets blocked through")
	r.e.herdr.edit(func() { r.e.herdr.pane("zz")["agent_status"] = "blocked" }) // a snoozed one being blocked is not news
	if l := r.sync()["label"]; l != "z snoozed only · 2 + 1 blocked" {
		t.Fatal(l)
	}
}

func TestRecordsWithNoAgentBehindThemHoldNoView(t *testing.T) {
	// a snoozed agent exited: the record stays (it may come back), the filter does not
	r := newSyncRig(t)
	r.e.herdr.edit(func() {
		r.e.herdr.pane("zz")["agent"] = nil
		r.e.herdr.pane("zz2")["agent"] = nil
	})
	r.visit()
	r.sync()
	sameJSON(t, r.e.herdr.currentView(), map[string]any{"active": true, "source": radar, "label": "active"}, "untouched")
	if r.state.View.Showing != "" {
		t.Fatal(r.state.View)
	}
}

func TestFailedWorkspaceTokenIsRetriedNextTime(t *testing.T) {
	r := newSyncRig(t)
	r.state.Workspaces["w1"] = &spaceEntry{Until: nowMS() + hourMS, Dirty: true}
	r.e.herdr.edit(func() {
		r.e.herdr.fail = func(method string, _ map[string]any) (string, bool) {
			if method == "workspace.report_metadata" {
				return "busy", false
			}
			return "", false
		}
	})
	r.sync()
	if !r.state.Workspaces["w1"].Dirty {
		t.Fatal("not marked for a retry")
	}
	r.e.herdr.edit(func() { r.e.herdr.fail = nil })
	r.e.herdr.clearCalls()
	r.sync()
	if r.e.herdr.count("workspace.report_metadata") != 1 || r.state.Workspaces["w1"].Dirty {
		t.Fatal(r.e.herdr.callList(), r.state.Workspaces["w1"])
	}
}

func TestRadarsOffFlagIsNotAppliedToAnotherPlugin(t *testing.T) {
	r := newSyncRig(t)
	r.sync() // borrowed from radar
	// radar's own record says its view is off…
	flag := filepath.Join(herdrStateRoot(), "hhdebb.herdr-radar", "agent-view.on")
	os.MkdirAll(filepath.Dir(flag), 0o700)
	os.WriteFile(flag, []byte("off\n"), 0o600)
	// …but the inbox holds the view now.
	r.e.herdr.edit(func() {
		r.e.herdr.view = map[string]any{"active": true, "source": "herdr-agent-inbox", "label": "Inbox"}
	})
	sameJSON(t, r.sync()["sort"], knownViews["herdr-agent-inbox"].sorts["Inbox"], "sort")
	if r.state.View.Inherited == nil || r.state.View.Inherited.Source != "herdr-agent-inbox" {
		t.Fatal(r.state.View)
	}
}

func TestRadarsOffFlagDropsItsSort(t *testing.T) {
	r := newSyncRig(t)
	r.sync()
	flag := filepath.Join(herdrStateRoot(), "hhdebb.herdr-radar", "agent-view.on")
	os.MkdirAll(filepath.Dir(flag), 0o700)
	os.WriteFile(flag, []byte("off\n"), 0o600)
	r.state.Panes["zz3"] = &paneEntry{Until: nowMS() + hourMS} // a change, so the view is re-set
	r.e.herdr.edit(func() { r.e.herdr.panes = append(r.e.herdr.panes, fpane("zz3", nil)) })
	// radar turned its view off: herdr's own order, not radar's
	sameJSON(t, r.sync()["sort"], panelModes["spaces"].sort, "sort")
}

func TestTimerArmedForTheSoonestDeadline(t *testing.T) {
	r := newSyncRig(t)
	spawned := 0
	stubSpawn(t, func(int64) (int, error) { spawned++; return os.Getpid(), nil }) // alive while we are
	r.sync()
	r.sync()
	if spawned != 1 {
		t.Fatal("armed again while that sleeper lives:", spawned)
	}
}
