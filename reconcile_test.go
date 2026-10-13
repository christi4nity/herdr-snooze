package main

import (
	"reflect"
	"sort"
	"testing"
)

var now0 = noon.UnixMilli()

func kinds(list []tokenOp) []string {
	var out []string
	for _, op := range list {
		out = append(out, op.Kind)
	}
	return out
}

// targets is each op's kind and target, sorted.
func targets(list []tokenOp) [][2]string {
	var out [][2]string
	for _, op := range list {
		out = append(out, [2]string{op.Kind, op.Target})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0]+out[i][1] < out[j][0]+out[j][1] })
	return out
}

func clearOf(pid string) tokenOp { return tokenOp{Kind: opClear, Target: pid} }

func expectOps(t *testing.T, got []tokenOp, want ...tokenOp) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ops %+v, want %+v", got, want)
	}
}

func rec(st *state, panes []pane, now int64) []tokenOp {
	return reconcile(st, panes, now, false, defaultBadge)
}

func TestNewSnoozeReportsTokenWithTTL(t *testing.T) {
	st := stateOf(map[string]*paneEntry{"p1": {Until: now0 + hourMS, Dirty: true}}, nil)
	expectOps(t, reconcile(st, []pane{mkPane("p1", nil)}, now0, false, "z"), tokenOp{Kind: opReport, Target: "p1", Value: "z 13:00", TTL: hourMS})
	if e := st.Panes["p1"]; e.TokenUntil != now0+hourMS || e.Dirty {
		t.Fatalf("%+v", e)
	}
}

func TestSteadyStateSendsNothing(t *testing.T) {
	st := stateOf(map[string]*paneEntry{"p1": {Until: now0 + hourMS, TokenUntil: now0 + hourMS}}, nil)
	expectOps(t, rec(st, []pane{mkPane("p1", liveToken("z"))}, now0))
}

func TestTTLIsCappedAndRefreshedBeforeItDies(t *testing.T) {
	st := stateOf(map[string]*paneEntry{"p1": {Until: now0 + 7*24*hourMS, Dirty: true}}, nil)
	if got := rec(st, []pane{mkPane("p1", nil)}, now0); got[0].TTL != maxTTLMS {
		t.Fatalf("%+v", got)
	}
	live := []pane{mkPane("p1", liveToken("z"))}
	expectOps(t, rec(st, live, now0+11*hourMS)) // 13h of token left
	later := now0 + 13*hourMS                   // 11h left: inside the refresh margin
	if got := kinds(rec(st, live, later)); !reflect.DeepEqual(got, []string{opReport}) {
		t.Fatal(got)
	}
	if st.Panes["p1"].TokenUntil != later+maxTTLMS {
		t.Fatal(st.Panes["p1"].TokenUntil)
	}
}

func TestMissingTokenIsRestored(t *testing.T) {
	// server restart, or the token expired under a >24h snooze
	st := stateOf(map[string]*paneEntry{"p1": {Until: now0 + hourMS, TokenUntil: now0 + hourMS}}, nil)
	if got := kinds(rec(st, []pane{mkPane("p1", nil)}, now0)); !reflect.DeepEqual(got, []string{opReport}) {
		t.Fatal(got)
	}
}

func TestForceReReportsEvenWhenPresent(t *testing.T) {
	st := stateOf(map[string]*paneEntry{"p1": {Until: now0 + hourMS, TokenUntil: now0 + hourMS}}, nil)
	got := reconcile(st, []pane{mkPane("p1", liveToken("z"))}, now0, true, defaultBadge)
	if !reflect.DeepEqual(kinds(got), []string{opReport}) {
		t.Fatal(got)
	}
}

func TestExpiredIsClearedAndForgottenOnce(t *testing.T) {
	st := stateOf(map[string]*paneEntry{"p1": {Until: now0 - 1}}, nil)
	expectOps(t, rec(st, []pane{mkPane("p1", liveToken("z"))}, now0), clearOf("p1")) // once, not twice
	if len(st.Panes) != 0 {
		t.Fatal(st.Panes)
	}
}

func TestClosedPaneIsForgottenSilently(t *testing.T) {
	st := stateOf(map[string]*paneEntry{"gone": {Until: now0 + hourMS}}, nil)
	expectOps(t, rec(st, []pane{mkPane("p1", nil)}, now0))
	if len(st.Panes) != 0 {
		t.Fatal(st.Panes)
	}
}

func TestTokenWithoutARecordIsCleared(t *testing.T) {
	// state file lost, or a run that died after tokens were sent
	st := stateOf(map[string]*paneEntry{"p1": {Until: now0 + hourMS, TokenUntil: now0 + hourMS}}, nil)
	live := []pane{mkPane("p1", liveToken("z 12:00")), mkPane("ghost", liveToken("z 12:00")),
		mkPane("old", map[string]any{token: "z 12:00"}), mkPane("plain", nil)} // "old": 0.1.x wrote no mark
	expectOps(t, reconcile(st, live, now0, false, "z"), clearOf("ghost"), clearOf("old"))
}

func snoozedClaude(extra func(*paneEntry)) *state {
	e := &paneEntry{Until: now0 + hourMS, TokenUntil: now0 + hourMS, AgentID: &agentID{"claude"}}
	if extra != nil {
		extra(e)
	}
	return stateOf(map[string]*paneEntry{"p1": e}, nil)
}

func TestANewAgentAfterTheOldOneExitedDoesNotInheritTheSnooze(t *testing.T) {
	st := snoozedClaude(nil)
	expectOps(t, rec(st, []pane{withAgent(mkPane("p1", liveToken("z")), "")}, now0))
	if st.Panes["p1"] == nil {
		t.Fatal("the record hides nothing while no agent runs: kept")
	}
	expectOps(t, rec(st, []pane{mkPane("p1", liveToken("z"))}, now0), clearOf("p1"))
	if len(st.Panes) != 0 {
		t.Fatal(st.Panes)
	}
}

func TestClearResumeAndCompactKeepTheSnooze(t *testing.T) {
	// The same running agent: pane.list says claude each time.
	st := snoozedClaude(nil)
	for range 4 {
		expectOps(t, rec(st, []pane{mkPane("p1", liveToken("z"))}, now0))
	}
	if st.Panes["p1"] == nil {
		t.Fatal("lost")
	}
}

func TestAnotherKindOfAgentIsANewAgent(t *testing.T) {
	st := snoozedClaude(nil)
	expectOps(t, rec(st, []pane{withAgent(mkPane("p1", liveToken("z")), "codex")}, now0), clearOf("p1"))
}

func TestANewAgentInASnoozedSpaceStaysHidden(t *testing.T) {
	// The space covers whatever runs in it: no clear-and-re-adopt flicker.
	st := stateOf(map[string]*paneEntry{"p1": {Until: now0 + hourMS, TokenUntil: now0 + hourMS, Via: viaWorkspace,
		WorkspaceID: "w1", AgentID: &agentID{"claude"}}},
		map[string]*spaceEntry{"w1": {Until: now0 + hourMS, TokenUntil: now0 + hourMS}})
	rec(st, []pane{withAgent(mkPane("p1", liveToken("z")), "")}, now0)
	expectOps(t, rec(st, []pane{withAgent(mkPane("p1", liveToken("z")), "codex")}, now0))
	if e := st.Panes["p1"]; e.AgentID.Agent != "codex" || e.AgentGone {
		t.Fatalf("%+v", e)
	}
}

func TestA01RecordAdoptsItsAgentAndGetsTheMarker(t *testing.T) {
	// upgrading: no identity recorded, and the token has no mark yet
	st := stateOf(map[string]*paneEntry{"p1": {Until: now0 + hourMS, TokenUntil: now0 + hourMS}}, nil)
	got := rec(st, []pane{mkPane("p1", map[string]any{token: "z 12:00"})}, now0)
	if !reflect.DeepEqual(kinds(got), []string{opReport}) {
		t.Fatal(got) // re-reported, now with the mark
	}
	if e := st.Panes["p1"]; e.AgentID == nil || e.AgentID.Agent != "claude" || e.TerminalID != "t-p1" {
		t.Fatalf("%+v", e)
	}
}

// ── v0.2: a moved pane gets a new ID and keeps its terminal ──

func TestAMovedPaneKeepsItsSnoozeWhateverTheHookOrder(t *testing.T) {
	st := snoozedClaude(func(e *paneEntry) { e.Via, e.WorkspaceID, e.TerminalID = viaPane, "w1", "t-p1" })
	got := rec(st, []pane{moved(mkPane("p1", nil), "w2:p1", "w2")}, now0) // the token stayed behind
	if ids := paneIDs(st); !reflect.DeepEqual(ids, []string{"w2:p1"}) {
		t.Fatal(ids)
	}
	if tg := targets(got); !reflect.DeepEqual(tg, [][2]string{{opReport, "w2:p1"}}) {
		t.Fatal("hidden again under its new ID:", tg)
	}
	if st.Panes["w2:p1"].WorkspaceID != "w2" {
		t.Fatal(st.Panes["w2:p1"])
	}
}

func TestTwoQuickMovesInAnyOrder(t *testing.T) {
	st := snoozedClaude(func(e *paneEntry) { e.Via, e.WorkspaceID, e.TerminalID = viaPane, "w1", "t-p1" })
	c := moved(mkPane("p1", nil), "C", "w3")
	rec(st, []pane{c}, now0)
	rec(st, []pane{c}, now0) // the late hooks for A → B and B → C
	if ids := paneIDs(st); !reflect.DeepEqual(ids, []string{"C"}) {
		t.Fatal(ids)
	}
}

func TestLeavingASnoozedSpaceKeepsItsOwnDeadline(t *testing.T) {
	st := stateOf(map[string]*paneEntry{"p1": {Until: now0 + hourMS, Via: viaWorkspace, WorkspaceID: "w1", TerminalID: "t-p1"}},
		map[string]*spaceEntry{"w1": {Until: now0 + hourMS, TokenUntil: now0 + hourMS}})
	rec(st, []pane{mkPane("stay", nil), moved(mkPane("p1", nil), "w2:p1", "w2")}, now0)
	if st.Panes["w2:p1"].Via != viaPane {
		t.Fatal(st.Panes["w2:p1"])
	}
}

func TestAWokenPaneStaysWokenWhenItMovesOutAndBack(t *testing.T) {
	st := stateOf(nil, map[string]*spaceEntry{"w1": {Until: now0 + hourMS, TokenUntil: now0 + hourMS, Except: []string{"p1"}}})
	rec(st, []pane{mkPane("p1", nil), inSpace(mkPane("other", nil), "w2")}, now0) // learns p1's terminal
	p := mkPane("p1", nil)
	rec(st, []pane{moved(p, "w2:p1", "w2"), mkPane("stay", nil)}, now0)
	rec(st, []pane{moved(p, "w1:p1-back", "w1"), mkPane("stay", nil)}, now0)
	if st.Panes["w1:p1-back"] != nil {
		t.Fatal("re-adopted")
	}
	if got := st.Workspaces["w1"].Except; !reflect.DeepEqual(got, []string{"w1:p1-back"}) {
		t.Fatal(got)
	}
}

func TestARestartGivesTerminalsNewIDsAndKnownPanesLearnThem(t *testing.T) {
	st := snoozedClaude(func(e *paneEntry) { e.Via, e.WorkspaceID, e.TerminalID = viaPane, "w1", "t-old" })
	p := mkPane("p1", liveToken("z"))
	p.TerminalID = "t-new"
	rec(st, []pane{p}, now0)
	if st.Panes["p1"].TerminalID != "t-new" {
		t.Fatal(st.Panes["p1"])
	}
	rec(st, []pane{moved(p, "w2:p1", "w2")}, now0)
	if ids := paneIDs(st); !reflect.DeepEqual(ids, []string{"w2:p1"}) {
		t.Fatal(ids)
	}
}

func TestAMoveThenARestartBeforeAnyTickLosesTheSnooze(t *testing.T) {
	// Documented limit: the pane then shows again (never wrongly hidden).
	st := snoozedClaude(func(e *paneEntry) { e.Via, e.WorkspaceID, e.TerminalID = viaPane, "w1", "t-p1" })
	p := moved(mkPane("p1", nil), "w2:p1", "w2")
	p.TerminalID = "t-restored"
	expectOps(t, rec(st, []pane{p}, now0))
	if len(st.Panes) != 0 {
		t.Fatal(st.Panes)
	}
}

// ── v0.2: herdr's pane.agent_detected tells a new process of the same kind ──

// detectedHook is the event as herdr 0.9.1 sends it: on a release, agent
// still names the agent that left.
func detectedHook(pid, agent string, released bool) map[string]any {
	data := map[string]any{"pane_id": pid, "workspace_id": "w1"}
	if agent != "" {
		data["agent"] = agent
	}
	if released {
		data["released"], data["final_status"] = true, "idle"
	}
	return map[string]any{"event": "pane.agent_detected", "data": data}
}

func detected(t *testing.T, pid, agent string, released bool) detectedEvent {
	t.Helper()
	ev, ok := findDetected(norm(detectedHook(pid, agent, released)))
	if !ok {
		t.Fatal("not found")
	}
	return ev
}

func since(ms int64) func(*paneEntry) { return func(e *paneEntry) { e.Since = ms } }

func TestAQuickRestartOfTheSameAgentDropsTheSnooze(t *testing.T) {
	st := snoozedClaude(since(now0 - hourMS))
	if !agentDetected(st, detected(t, "p1", "claude", true), now0) {
		t.Fatal("not marked")
	}
	expectOps(t, rec(st, []pane{mkPane("p1", liveToken("z"))}, now0), clearOf("p1"))
}

func TestAReleaseRightAfterSnoozingStillCounts(t *testing.T) {
	st := snoozedClaude(since(now0 - 2000))
	if !agentDetected(st, detected(t, "p1", "claude", true), now0) {
		t.Fatal("not marked")
	}
	expectOps(t, rec(st, []pane{mkPane("p1", liveToken("z"))}, now0), clearOf("p1"))
}

func TestTheAgentLeavingRightAfterSnoozingCounts(t *testing.T) {
	st := snoozedClaude(since(now0 - 2000))
	if !agentDetected(st, detected(t, "p1", "", false), now0) {
		t.Fatal("not marked")
	}
}

func TestALateDetectionOfTheSnoozedAgentItselfIsIgnored(t *testing.T) {
	st := snoozedClaude(since(now0 - 2000))
	if agentDetected(st, detected(t, "p1", "claude", false), now0) {
		t.Fatal("marked")
	}
	expectOps(t, rec(st, []pane{mkPane("p1", liveToken("z"))}, now0))
}

func TestAnAgentArrivingAfterTheGraceDropsTheSnooze(t *testing.T) {
	st := snoozedClaude(since(now0 - hourMS))
	if !agentDetected(st, detected(t, "p1", "claude", false), now0) {
		t.Fatal("not marked")
	}
}

func TestAReleaseFromBeforeTheSnoozeIsNotAboutIt(t *testing.T) {
	st := snoozedClaude(since(now0))
	if agentDetected(st, detected(t, "p1", "claude", true), now0-500) || st.Panes["p1"].AgentGone {
		t.Fatal("marked")
	}
}

func TestAnArrivalIsDatedByWhenItsHookStarted(t *testing.T) {
	st := snoozedClaude(since(now0 - hourMS))
	if agentDetected(st, detected(t, "p1", "claude", false), now0-hourMS+2000) {
		t.Fatal("marked")
	}
}

func TestDetectionsElsewhereChangeNothing(t *testing.T) {
	st := snoozedClaude(since(now0 - hourMS))
	if agentDetected(st, detected(t, "p2", "claude", true), now0) || st.Panes["p1"].AgentGone {
		t.Fatal("marked")
	}
}

func TestTheDetectedPaneIsFoundInHerdrsEvent(t *testing.T) {
	ev := detected(t, "w1:p3", "claude", true)
	if ev != (detectedEvent{"w1:p3", "claude", true}) {
		t.Fatal(ev)
	}
	if _, ok := findDetected(norm(map[string]any{"event": "x"})); ok {
		t.Fatal("found in nothing")
	}
	if _, ok := findDetected(nil); ok {
		t.Fatal("found in nil")
	}
}

func TestAClosedPaneIsNotMistakenForAMovedOne(t *testing.T) {
	st := snoozedClaude(func(e *paneEntry) { e.TerminalID = "t-p1" })
	expectOps(t, rec(st, []pane{mkPane("p2", nil)}, now0))
	if len(st.Panes) != 0 {
		t.Fatal(st.Panes)
	}
}

func TestAnotherPluginsTokenOfTheSameNameIsLeftAlone(t *testing.T) {
	live := []pane{mkPane("theirs", map[string]any{token: "yes"}), mkPane("ours", map[string]any{token: "z 12:00"})}
	expectOps(t, reconcile(emptyState(), live, now0, false, "z"), clearOf("ours"))
}

func TestWorkspaceAdoptsItsPanesIncludingLaterOnes(t *testing.T) {
	st := stateOf(nil, map[string]*spaceEntry{"w1": {Until: now0 + hourMS, Dirty: true}})
	live := []pane{mkPane("p1", nil), withAgent(mkPane("shell", nil), ""), inSpace(mkPane("other", nil), "w2")}
	if got := targets(rec(st, live, now0)); !reflect.DeepEqual(got, [][2]string{{opReport, "p1"}, {opWsReport, "w1"}}) {
		t.Fatal(got)
	}
	if st.Panes["p1"].Via != viaWorkspace || st.Panes["other"] != nil || st.Panes["shell"] != nil {
		t.Fatal(st.Panes) // a token on a shell hides nothing and makes counts lie
	}
	live[0].Tokens = liveToken("z")
	live = append(live, mkPane("p3", nil)) // an agent started later in the snoozed space
	if got := targets(rec(st, live, now0)); !reflect.DeepEqual(got, [][2]string{{opReport, "p3"}}) {
		t.Fatal(got)
	}
	live[3].Tokens = liveToken("z")
	live[1].Agent = "claude" // an agent detected later in a pane that was a shell
	if got := targets(rec(st, live, now0)); !reflect.DeepEqual(got, [][2]string{{opReport, "shell"}}) {
		t.Fatal(got)
	}
}

func TestSpaceWithNoPanesLeftGetsItsBadgeCleared(t *testing.T) {
	st := stateOf(nil, map[string]*spaceEntry{"empty": {Until: now0 + hourMS, TokenUntil: now0 + hourMS}})
	expectOps(t, rec(st, []pane{mkPane("p1", nil)}, now0), tokenOp{Kind: opWsClear, Target: "empty"})
	if len(st.Workspaces) != 0 {
		t.Fatal(st.Workspaces)
	}
}

func TestOwnShorterDeadlineInsideASnoozedSpaceIsNotOverruled(t *testing.T) {
	// space for 3h, one of its agents for 15 minutes: when the 15 minutes are
	// up it must stay visible, not get re-adopted for the rest of the 3h
	st := stateOf(map[string]*paneEntry{"p1": {Until: now0 + 900_000, Via: viaPane, TokenUntil: now0 + 900_000}},
		map[string]*spaceEntry{"w1": {Until: now0 + 3*hourMS, TokenUntil: now0 + 3*hourMS, Except: []string{"p1"}}})
	later := now0 + 1_000_000
	expectOps(t, rec(st, []pane{mkPane("p1", liveToken("z"))}, later), clearOf("p1"))
	expectOps(t, rec(st, []pane{mkPane("p1", nil)}, later+1000))
	if st.Panes["p1"] != nil {
		t.Fatal("re-adopted")
	}
}

func TestWorkspaceDoesNotReadoptAPaneWokenEarly(t *testing.T) {
	st := stateOf(nil, map[string]*spaceEntry{"w1": {Until: now0 + hourMS, TokenUntil: now0 + hourMS, Except: []string{"p1"}}})
	rec(st, []pane{mkPane("p1", nil), mkPane("p2", nil)}, now0)
	if ids := paneIDs(st); !reflect.DeepEqual(ids, []string{"p2"}) {
		t.Fatal(ids)
	}
}

func TestExpiredWorkspaceClearsItsTokenAndPanes(t *testing.T) {
	until := now0 - 1
	st := stateOf(map[string]*paneEntry{"p1": {Until: until, Via: viaWorkspace, WorkspaceID: "w1"}},
		map[string]*spaceEntry{"w1": {Until: until}})
	if got := targets(rec(st, []pane{mkPane("p1", nil)}, now0)); !reflect.DeepEqual(got, [][2]string{{opClear, "p1"}, {opWsClear, "w1"}}) {
		t.Fatal(got)
	}
	if !st.idle() {
		t.Fatal("not idle")
	}
}
