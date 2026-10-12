package main

import (
	"testing"
)

func snoozedOne() *state {
	return stateOf(map[string]*paneEntry{"p1": {Until: 1}}, nil)
}

var (
	free     = viewOwner{}
	ownView  = func() viewOwner { return viewOwner{Active: true, Source: source()} }
	radarIs  = func(label string) viewOwner { return viewOwner{Active: true, Source: radar, Label: label} }
	inboxOwn = viewOwner{Active: true, Source: "herdr-agent-inbox", Label: "Inbox"}
)

// plan is planView with the badge "z" and no panel mode, unless p says
// otherwise.
func plan(st *state, owner viewOwner, count int, adjust ...func(*viewPlan)) *viewAction {
	p := viewPlan{owner: owner, count: count, cfg: config{Badge: "z"}}
	for _, f := range adjust {
		f(&p)
	}
	return planView(st, p)
}

func mode(m string) func(*viewPlan)     { return func(p *viewPlan) { p.mode = m } }
func ownerOff(p *viewPlan)              { p.ownerOff = true }
func pinned(sort []any) func(*viewPlan) { return func(p *viewPlan) { p.cfg.Sort = sort } }
func taught(v map[string]map[string][]any) func(*viewPlan) {
	return func(p *viewPlan) { p.cfg.Views = v }
}

func expectSet(t *testing.T, a *viewAction) map[string]any {
	t.Helper()
	if a == nil || a.Method != "agent.view.set" {
		t.Fatalf("want a set, got %+v", a)
	}
	return a.Params
}

func expectClear(t *testing.T, a *viewAction) {
	t.Helper()
	if a == nil || a.Method != "agent.view.clear" {
		t.Fatalf("want a clear, got %+v", a)
	}
	sameJSON(t, a.Params, map[string]any{"source": source()}, "clear")
}

func TestTakesAFreeViewAndClearsItAfterwards(t *testing.T) {
	st := snoozedOne()
	p := expectSet(t, plan(st, free, 1))
	sameJSON(t, []any{p["source"], p["filter"], p["sort"], p["label"]}, []any{source(), viewFilter, []any{}, "z 1"}, "set")
	st.Panes = map[string]*paneEntry{}
	expectClear(t, plan(st, ownView(), 0))
	if !st.View.empty() {
		t.Fatal(st.View)
	}
}

func TestKeepsHerdrsOwnOrderAndLabelOnAFreeView(t *testing.T) {
	for m, label := range map[string]string{"priority": "priority z 1", "spaces": "grouped z 1"} {
		st := snoozedOne()
		p := expectSet(t, plan(st, free, 1, mode(m)))
		sameJSON(t, []any{p["label"], p["sort"]}, []any{label, panelModes[m].sort}, m)
		expectClear(t, plan(st, ownView(), 0, mode(m)))
	}
}

func TestABorrowedViewIgnoresHerdrsOwnOrder(t *testing.T) {
	p := expectSet(t, plan(snoozedOne(), radarIs("active"), 1, mode("priority")))
	sameJSON(t, []any{p["label"], p["sort"]}, []any{"active z 1", knownViews[radar].sorts["active"]}, "borrowed")
}

func TestBorrowsRadarsSortAndHandsTheViewBack(t *testing.T) {
	st := snoozedOne()
	p := expectSet(t, plan(st, radarIs("active"), 1))
	sameJSON(t, []any{p["sort"], p["label"], p["filter"]}, []any{knownViews[radar].sorts["active"], "active z 1", viewFilter}, "borrowed")
	st.Panes = map[string]*paneEntry{}
	back := expectSet(t, plan(st, ownView(), 0))
	sameJSON(t, back, map[string]any{"source": radar, "label": "active", "sort": knownViews[radar].sorts["active"]}, "handed back")
}

func TestIdempotentWhileWeHoldIt(t *testing.T) {
	st := snoozedOne()
	plan(st, radarIs("active"), 1)
	if a := plan(st, ownView(), 1); a != nil {
		t.Fatalf("re-set: %+v", a)
	}
	// the count is part of the label, so a second snooze re-sets the view
	expectSet(t, plan(st, ownView(), 2))
}

func TestFollowsRadarWhenItRetakesTheViewInAnotherMode(t *testing.T) {
	st := snoozedOne()
	plan(st, radarIs("active"), 1)
	p := expectSet(t, plan(st, radarIs("recent"), 1))
	sameJSON(t, p["sort"], knownViews[radar].sorts["recent"], "sort")
}

func TestReappliesAfterTheViewWasLost(t *testing.T) {
	st := snoozedOne()
	plan(st, ownView(), 1)
	expectSet(t, plan(st, free, 1))
}

func TestOwnerTurnedOffDropsTheBorrowedSort(t *testing.T) {
	st := snoozedOne()
	plan(st, radarIs("active"), 1)
	p := expectSet(t, plan(st, ownView(), 1, ownerOff))
	sameJSON(t, p["sort"], []any{}, "sort")
	st.Panes = map[string]*paneEntry{}
	expectClear(t, plan(st, ownView(), 0, ownerOff))
}

func TestUnknownOwnerIsClearedNotImpersonated(t *testing.T) {
	st := snoozedOne()
	p := expectSet(t, plan(st, viewOwner{Active: true, Source: "plugin:other", Label: "x"}, 1))
	sameJSON(t, p["sort"], []any{}, "sort")
	st.Panes = map[string]*paneEntry{}
	expectClear(t, plan(st, ownView(), 0))
}

func TestBorrowsAgentInboxSortAndHandsItBack(t *testing.T) {
	want := knownViews["herdr-agent-inbox"].sorts["Inbox"]
	st := snoozedOne()
	sameJSON(t, expectSet(t, plan(st, inboxOwn, 1))["sort"], want, "sort")
	st.Panes = map[string]*paneEntry{}
	sameJSON(t, expectSet(t, plan(st, ownView(), 0)), map[string]any{"source": "herdr-agent-inbox", "label": "Inbox", "sort": want}, "back")
}

func TestConfigViewsTeachAnUnknownOwner(t *testing.T) {
	other := viewOwner{Active: true, Source: "plugin:other", Label: "x"}
	views := map[string]map[string][]any{"plugin:other": {"x": {map[string]any{"field": "status", "order": "asc"}}}}
	st := snoozedOne()
	sameJSON(t, expectSet(t, plan(st, other, 1, taught(views)))["sort"], views["plugin:other"]["x"], "sort")
	st.Panes = map[string]*paneEntry{}
	if back := expectSet(t, plan(st, ownView(), 0, taught(views))); back["source"] != "plugin:other" {
		t.Fatal(back)
	}
	// (0.2 also checked a malformed table is ignored here; in Go, loadConfig
	// refuses one, see TestSortKeysMustBeOnesHerdrAccepts.)
}

func TestToggleShowsOnlySnoozedKeepingTheBorrowedSort(t *testing.T) {
	st := snoozedOne()
	hidden := expectSet(t, plan(st, radarIs("active"), 2))
	sameJSON(t, []any{hidden["filter"], hidden["label"]}, []any{viewFilter, "active z 2"}, "hidden")

	st.View.Showing = showingSnoozed
	shown := expectSet(t, plan(st, ownView(), 2)) // the filter is part of the fingerprint
	sameJSON(t, []any{shown["filter"], shown["label"], shown["sort"]},
		[]any{snoozedListFilter, "z snoozed only · 2", hidden["sort"]}, "shown")
	if st.View.Showing != showingSnoozed {
		t.Fatal("doesn't survive the next tick")
	}
	if a := plan(st, ownView(), 2); a != nil {
		t.Fatal(a)
	}
	st.View.Showing = ""
	sameJSON(t, expectSet(t, plan(st, ownView(), 2))["filter"], viewFilter, "back")
}

func TestToggleEndsWithTheLastSnooze(t *testing.T) {
	st := snoozedOne()
	plan(st, radarIs("active"), 1)
	st.View.Showing = showingSnoozed
	plan(st, ownView(), 1)
	st.Panes = map[string]*paneEntry{}
	back := expectSet(t, plan(st, ownView(), 0))
	if back["source"] != radar || back["filter"] != nil {
		t.Fatal("not handed back, or left on an empty list:", back)
	}
	if !st.View.empty() {
		t.Fatal(st.View)
	}
}

func TestConfigSortWins(t *testing.T) {
	sort := []any{map[string]any{"field": "status", "order": "asc"}}
	sameJSON(t, expectSet(t, plan(snoozedOne(), radarIs("active"), 1, pinned(sort)))["sort"], sort, "sort")
}

func TestLeavesSomeoneElsesViewAloneWhenNothingIsSnoozed(t *testing.T) {
	if a := plan(emptyState(), radarIs("active"), 0); a != nil {
		t.Fatal(a)
	}
}

// ── the snoozed list ends by itself; the real sequences ──

func showing() *state {
	st := stateOf(map[string]*paneEntry{"zz": {Until: 1}}, nil)
	st.View = viewRecord{Showing: showingSnoozed, FromPane: "work"}
	return st
}

func TestStaysWhileYouAreStillWhereYouOpenedIt(t *testing.T) {
	st := showing()
	if settleShowing(st, "work") || st.View.Showing != showingSnoozed {
		t.Fatal(st.View)
	}
}

func TestStaysWhileYouLookAtSnoozedAgents(t *testing.T) {
	st := showing()
	if settleShowing(st, "zz") || st.View.Showing != showingSnoozed {
		t.Fatal(st.View)
	}
}

func TestReturnsWhenFocusMovesToAnActivePane(t *testing.T) {
	st := showing()
	if !settleShowing(st, "elsewhere") || st.View.Showing != "" || st.View.FromPane != "" {
		t.Fatal(st.View)
	}
}

func TestGoingBackToTheOriginAfterVisitingASnoozedAgentAlsoReturns(t *testing.T) {
	st := showing()
	settleShowing(st, "zz")
	if !settleShowing(st, "work") {
		t.Fatal(st.View)
	}
}

func TestWakingTheAgentYouAreOnReturns(t *testing.T) {
	st := showing()
	settleShowing(st, "zz")
	st.Panes = map[string]*paneEntry{"other": {Until: 1}} // zz was woken; others remain
	if !settleShowing(st, "zz") {
		t.Fatal(st.View)
	}
}

func TestNoFocusInformationChangesNothing(t *testing.T) {
	if settleShowing(showing(), "") {
		t.Fatal("settled on no focus")
	}
	if settleShowing(stateOf(map[string]*paneEntry{"zz": {Until: 1}}, nil), "elsewhere") {
		t.Fatal("settled when not showing at all")
	}
}

func TestPlanViewKeepsTheOriginAcrossTicks(t *testing.T) {
	st := showing()
	plan(st, ownView(), 1)
	if st.View.FromPane != "work" || st.View.Showing != showingSnoozed {
		t.Fatal(st.View)
	}
}
