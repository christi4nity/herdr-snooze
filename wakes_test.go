package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAnAgentWhoseTimeIsUpSaysItIsAwake(t *testing.T) {
	e := cmdEnv(t)
	e.herdr.edit(func() { e.herdr.pane("zz")["title"] = "fix flaky tests" })
	e.snoozed(t, nowMS()-1000)
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if n := e.herdr.notifications(); !reflect.DeepEqual(n, []string{"fix flaky tests is awake: claude · One"}) {
		t.Fatal(n)
	}
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if n := e.herdr.notifications(); len(n) != 1 {
		t.Fatalf("said twice: %v", n)
	}
}

func TestAConditionalWakeSaysWhatHappened(t *testing.T) {
	e := cmdEnv(t)
	e.write(t, fmt.Sprintf(`{"panes": {"zz": {"until": %d, "via": "pane", "when": "done", "status": "working"}}}`, nowMS()+hourMS))
	e.herdr.edit(func() { e.herdr.pane("zz")["agent_status"] = "done" })
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if n := e.herdr.notifications(); !reflect.DeepEqual(n, []string{"claude is awake: It's done · claude · One"}) {
		t.Fatal(n)
	}
}

func TestAWakeYouAskedForIsNotAnnounced(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()+hourMS)
	mustRun(t, "wake", "zz")
	if n := e.herdr.notifications(); len(n) != 0 {
		t.Fatal(n)
	}
}

func TestAClosedPaneIsNotAnnounced(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()-1000)
	e.herdr.edit(func() { e.herdr.panes = e.herdr.panes[:1] })
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if n := e.herdr.notifications(); len(n) != 0 {
		t.Fatal(n)
	}
}

func TestWakeToastsCanBeTurnedOff(t *testing.T) {
	e := cmdEnv(t)
	writeConfig(t, `{"notify_wake": false}`)
	e.snoozed(t, nowMS()-1000)
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if n := e.herdr.notifications(); len(n) != 0 {
		t.Fatal(n)
	}
}

func TestASpaceWakesAsOne(t *testing.T) {
	labels := map[string]string{"w1": "backend"}
	woke := []woken{
		{Name: "api", Agent: "claude", WorkspaceID: "w1", Space: true},
		{Name: "docs", Agent: "codex", WorkspaceID: "w1", Space: true},
	}
	title, body := wakeToast(woke, []string{"w1"}, labels)
	if title != "backend is awake" || body != "2 agents: api, docs" {
		t.Errorf("%q %q", title, body)
	}
	woke[1] = woken{Name: "docs", WorkspaceID: "w2", Reason: "it needs you"}
	title, body = wakeToast(woke, []string{"w1"}, labels)
	if title != "2 agents are awake" || body != "api, docs (it needs you)" {
		t.Errorf("%q %q", title, body)
	}
}

func TestWhatWokeIsOnTheJustWokeListWithAClock(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()-1000)
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if readState().Woke["zz"] == nil {
		t.Fatal("not on the list")
	}
	tokens := e.herdr.tokens("zz")
	if tokens[wokeBadge] != defaultWokeBadge || tokens[wokeToken] == nil {
		t.Fatalf("tokens %v", tokens)
	}
	// Going to it takes it off, and the clock with it.
	e.herdr.focus("zz")
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if readState().Woke["zz"] != nil {
		t.Fatal("still on the list")
	}
	if _, ok := e.herdr.tokens("zz")[wokeBadge]; ok {
		t.Fatal("clock left")
	}
	if exists(e.file()) {
		t.Fatal("file left with nothing in it")
	}
}

func TestSnoozingAgainTakesItOffTheJustWokeList(t *testing.T) {
	st := emptyState()
	p := agentPane("p", "idle", map[string]any{wokeToken: "14:05", wokeBadge: defaultWokeBadge})
	st.Woke["p"] = &wokeEntry{At: nowMS(), AgentID: agentIdentity(p)}
	st.Panes["p"] = &paneEntry{Until: nowMS() + hourMS}
	ops := trackWoke(st, []pane{p}, nowMS(), false, defaultWokeBadge)
	if len(st.Woke) != 0 || len(ops) != 1 || ops[0].Kind != opWokeClear {
		t.Fatalf("%v %+v", st.Woke, ops)
	}
}

func TestTheJustWokeListLastsAWhile(t *testing.T) {
	st := emptyState()
	p := agentPane("p", "idle", nil)
	st.Woke["p"] = &wokeEntry{At: nowMS() - justWokeMS, AgentID: agentIdentity(p)}
	trackWoke(st, []pane{p}, nowMS(), false, defaultWokeBadge)
	if len(st.Woke) != 0 {
		t.Fatal("kept past its time")
	}
}

func TestTheListShowsWhatJustWokeFirst(t *testing.T) {
	st := emptyState()
	st.Woke["b"] = &wokeEntry{At: nowMS(), Reason: "it's done"}
	st.Panes["c"] = &paneEntry{Until: nowMS() + hourMS, Via: viaPane}
	rows := buildRows(st, []pane{agentPane("a", "idle", nil), agentPane("b", "done", nil), agentPane("c", "idle", nil)}, nil)
	var got []string
	for _, r := range rows {
		got = append(got, r.id)
	}
	want := []string{"Just woke", "b", "", "Snoozed", "c", "", "Awake", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestTheAgentYouStayedOnStaysOnTheListTillYouComeBack(t *testing.T) {
	e := cmdEnv(t)
	e.herdr.focus("zz") // snoozed where you were, and you stayed
	e.snoozed(t, nowMS()-1000)
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if readState().Woke["zz"] == nil || e.herdr.tokens("zz")[wokeBadge] != defaultWokeBadge {
		t.Fatal("left the list as it woke")
	}
	e.herdr.focus("work")
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if readState().Woke["zz"] == nil {
		t.Fatal("left the list when you went elsewhere")
	}
	e.herdr.focus("zz")
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if readState().Woke["zz"] != nil {
		t.Fatal("still listed once you came back to it")
	}
}

func TestTheClockGoesWhereTheSnoozeBadgeWas(t *testing.T) {
	e := cmdEnv(t)
	writeConfig(t, `{"badge": ""}`) // with no badge, any `snoozed` looks like ours
	e.snoozed(t, nowMS()-1000)
	for range 2 {
		if err := tick(nil); err != nil {
			t.Fatal(err)
		}
		if got := e.herdr.tokens("zz")[token]; got != defaultWokeBadge {
			t.Fatalf("snoozed = %v", got)
		}
	}
}

func TestSnoozedAgainTheClockGivesWayToTheSnooze(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()-1000)
	if err := tick(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := snooze("pane", "zz", time.Now().Add(time.Hour), loadConfig(), nil, ""); err != nil {
		t.Fatal(err)
	}
	tokens := e.herdr.tokens("zz")
	if v, _ := tokens[token].(string); v == "" || strings.Contains(v, defaultWokeBadge) || tokens[wokeBadge] != nil || tokens[mark] == nil {
		t.Fatalf("tokens %v", tokens)
	}
}
