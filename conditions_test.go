package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func agentPane(id, status string, tokens map[string]any) pane {
	if tokens == nil {
		tokens = map[string]any{}
	}
	return pane{PaneID: id, WorkspaceID: "w1", TerminalID: "t-" + id, Agent: "claude", AgentStatus: status, Tokens: tokens}
}

func conditionState(e *paneEntry) *state {
	st := emptyState()
	e.Until, e.Via, e.WorkspaceID = nowMS()+hourMS, viaPane, "w1"
	st.Panes["p"] = e
	return st
}

func TestUntilDoneWakesWhenItStopsWorking(t *testing.T) {
	st := conditionState(&paneEntry{When: whenDone, Status: "working"})
	wakeOnChange(st, []pane{agentPane("p", "working", nil)}, true)
	if st.Panes["p"] == nil {
		t.Fatal("woke while it was still working")
	}
	wakeOnChange(st, []pane{agentPane("p", "done", nil)}, true)
	if st.Panes["p"] != nil {
		t.Fatal("still snoozed once done")
	}
}

func TestUntilDoneNeedsToHaveSeenItWorking(t *testing.T) {
	// No status recorded (a snooze from the command line): the first look
	// only learns it.
	st := conditionState(&paneEntry{When: whenDone})
	wakeOnChange(st, []pane{agentPane("p", "idle", nil)}, true)
	if st.Panes["p"] == nil || st.Panes["p"].Status != "idle" {
		t.Fatalf("%+v", st.Panes["p"])
	}
}

func TestUntilMergedWakesWhenThePullRequestIsMergedOrClosed(t *testing.T) {
	for _, state := range []string{"merged", "closed"} {
		st := conditionState(&paneEntry{When: whenMerged, Status: "idle"})
		wakeOnChange(st, []pane{agentPane("p", "idle", map[string]any{"pr": "#4", "pr_state": "open"})}, true)
		if st.Panes["p"] == nil {
			t.Fatal("woke while it was open")
		}
		wakeOnChange(st, []pane{agentPane("p", "idle", map[string]any{"pr": "#4", "pr_state": state})}, true)
		if st.Panes["p"] != nil {
			t.Errorf("still snoozed once %s", state)
		}
	}
}

func TestAnySnoozeWakesWhenTheAgentComesToNeedYou(t *testing.T) {
	st := conditionState(&paneEntry{Status: "working"})
	wakeOnChange(st, []pane{agentPane("p", "blocked", nil)}, true)
	if st.Panes["p"] != nil {
		t.Fatal("still hidden while it needs you")
	}
	// Unless that's turned off.
	st = conditionState(&paneEntry{Status: "working"})
	wakeOnChange(st, []pane{agentPane("p", "blocked", nil)}, false)
	if st.Panes["p"] == nil {
		t.Fatal("woke with wake_when_blocked off")
	}
}

func TestAnAgentSnoozedWhileItNeededYouStaysSnoozed(t *testing.T) {
	st := conditionState(&paneEntry{Status: "blocked"})
	wakeOnChange(st, []pane{agentPane("p", "blocked", nil)}, true)
	if st.Panes["p"] == nil {
		t.Fatal("woke an agent snoozed knowing it was waiting")
	}
	// Until it moves on, and gets stuck again.
	wakeOnChange(st, []pane{agentPane("p", "working", nil)}, true)
	wakeOnChange(st, []pane{agentPane("p", "blocked", nil)}, true)
	if st.Panes["p"] != nil {
		t.Fatal("stuck again, still hidden")
	}
}

func TestWokenOutOfASnoozedSpaceIsNotReadopted(t *testing.T) {
	st := conditionState(&paneEntry{Status: "working"})
	st.Panes["p"].Via = viaWorkspace
	st.Workspaces["w1"] = &spaceEntry{Until: nowMS() + hourMS}
	panes := []pane{agentPane("p", "blocked", nil)}
	wakeOnChange(st, panes, true)
	reconcile(st, panes, nowMS(), false, defaultBadge)
	if st.Panes["p"] != nil {
		t.Fatal("the space took it back")
	}
}

func TestAConditionalSnoozeSaysWhatItWaitsFor(t *testing.T) {
	st := conditionState(&paneEntry{When: whenDone, Status: "working", Dirty: true})
	ops := reconcile(st, []pane{agentPane("p", "working", nil)}, nowMS(), false, defaultBadge)
	if len(ops) != 1 || !strings.HasSuffix(ops[0].Value, "till done") {
		t.Errorf("%+v", ops)
	}
}

func TestTheWakeIsSentThroughHerdr(t *testing.T) {
	e := cmdEnv(t)
	e.write(t, fmt.Sprintf(`{"panes": {"zz": {"until": %d, "via": "pane", "status": "working"}}}`, nowMS()+hourMS))
	e.herdr.edit(func() { e.herdr.pane("zz")["agent_status"] = "blocked" })
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if readState().Panes["zz"] != nil {
		t.Error("record kept")
	}
	if _, hidden := e.herdr.tokens("zz")[mark]; hidden {
		t.Error("token kept: still hidden")
	}
}

func TestConditionsAreCappedAtTomorrowMorning(t *testing.T) {
	now := time.Date(2026, 10, 13, 14, 5, 0, 0, time.Local)
	if got := conditionCap(now); !got.Equal(time.Date(2026, 10, 14, 9, 0, 0, 0, time.Local)) {
		t.Error(got)
	}
}

func TestThePickerOffersOnlyWhatFits(t *testing.T) {
	stopClock(t)
	// Working, with a pull request open: both.
	m := demoScreen(newModel(defaultConfig(), newDemoWorld(), target{}, false, "w1:p2", nil), 52, 20)
	v := m.View()
	if !strings.Contains(v, "until done") || !strings.Contains(v, "until #482 is merged") || !strings.Contains(v, "or Wed 09:00") {
		t.Errorf("view:\n%s", v)
	}
	// Done, no pull request: neither.
	m = demoScreen(newModel(defaultConfig(), newDemoWorld(), target{}, false, "w2:p1", nil), 52, 20)
	if v := m.View(); strings.Contains(v, "until done") || strings.Contains(v, "merged") {
		t.Errorf("view:\n%s", v)
	}
}

func TestPickingUntilDoneSnoozesTillDone(t *testing.T) {
	stopClock(t)
	w := &openWorld{demoWorld: newDemoWorld()}
	m := demoScreen(newModel(defaultConfig(), w, target{}, false, "w1:p2", nil), 52, 20)
	m, cmd := key(t, m, "d")
	runCmd(t, m, cmd)
	e := w.st.Panes["w1:p2"]
	if e == nil || e.When != whenDone || !fromMS(e.Until).Equal(conditionCap(timeNow())) {
		t.Errorf("%+v", e)
	}
}
