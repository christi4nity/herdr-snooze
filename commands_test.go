package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// cmdEnv is herdr running the plugin: a real state dir, a fake herdr whose
// view radar holds, "work" focused and active, "zz" beside it.
func cmdEnv(t *testing.T) *env {
	t.Helper()
	panes := []map[string]any{fpane("work", map[string]any{"focused": true}), fpane("zz", nil)}
	return newEnv(t, panes, map[string]any{"active": true, "source": radar, "label": "active"})
}

func freeEnv(t *testing.T) *env {
	e := cmdEnv(t)
	e.herdr.edit(func() { e.herdr.view = map[string]any{"active": false} })
	return e
}

func invokedOn(t *testing.T, paneID, workspaceID string) {
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", fmt.Sprintf(`{"focused_pane_id": %q, "workspace_id": %q}`, paneID, workspaceID))
}

func lockFile(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	return func() { f.Close() }
}

func TestTickWithNothingSnoozedReturnsWithoutTouchingHerdr(t *testing.T) {
	e := cmdEnv(t)
	os.RemoveAll(filepath.Join(os.Getenv("SNOOZE_STATE_DIR")))
	for _, args := range [][]string{{"tick"}, {"tick", "--force"}, {"tick", "--timer"}, {"tick", "--detected"}} {
		mustRun(t, args...)
	}
	if calls := e.herdr.callList(); len(calls) != 0 {
		t.Fatal(calls)
	}
	if exists(os.Getenv("SNOOZE_STATE_DIR")) || e.timesListed() != 0 {
		t.Fatal("did work while idle")
	}
}

func TestSnoozeKeyAsksHowLongOnAnActiveAgent(t *testing.T) {
	e := cmdEnv(t)
	invokedOn(t, "work", "w1")
	if code, err := runAction("agent"); code != 0 || err != nil {
		t.Fatal(code, err)
	}
	opened := e.herdr.paramsOf("plugin.pane.open")
	if len(opened) != 1 || opened[0]["env"].(map[string]any)[envTarget] != "work" {
		t.Fatal(opened)
	}
	if !readState().idle() {
		t.Fatal("snoozed or woke something")
	}
}

func TestSameKeyWakesASnoozedAgentWithoutAPopup(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()+hourMS)
	invokedOn(t, "zz", "w1")
	if code, err := runAction("agent"); code != 0 || err != nil {
		t.Fatal(code, err)
	}
	if e.herdr.count("plugin.pane.open") != 0 || readState().Panes["zz"] != nil {
		t.Fatal(e.herdr.callList(), readState().Panes)
	}
}

func TestSpaceActionWakesASnoozedSpace(t *testing.T) {
	e := cmdEnv(t)
	e.write(t, fmt.Sprintf(`{"workspaces": {"w1": {"until": %d}}}`, nowMS()+hourMS))
	invokedOn(t, "work", "w1")
	if code, err := runAction("workspace"); code != 0 || err != nil {
		t.Fatal(code, err)
	}
	if e.herdr.count("plugin.pane.open") != 0 || readState().Workspaces["w1"] != nil {
		t.Fatal(e.herdr.callList(), readState().Workspaces)
	}
}

func TestSpaceActionAsksHowLongOnAnActiveSpace(t *testing.T) {
	e := cmdEnv(t)
	invokedOn(t, "work", "w1")
	if code, err := runAction("workspace"); code != 0 || err != nil {
		t.Fatal(code, err)
	}
	opened := e.herdr.paramsOf("plugin.pane.open")
	if len(opened) != 1 {
		t.Fatal(e.herdr.callList())
	}
	env := opened[0]["env"].(map[string]any)
	if env[envKind] != "workspace" || env[envTarget] != "w1" || env[envLabel] != "One" {
		t.Fatal(env)
	}
}

// ── v0.2: moved panes, a stale picker, bursts of hooks ──

func TestAMovedPaneKeepsItsSnoozeEvenIfAPlainTickSeesItFirst(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()+hourMS)
	mustRun(t, "tick") // learns zz's terminal
	e.herdr.edit(func() {
		p := e.herdr.pane("zz")
		p["pane_id"], p["workspace_id"], p["tokens"] = "w2:zz", "w2", map[string]any{} // the token stayed behind
	})
	mustRun(t, "tick") // a focus tick, no event
	if ids := paneIDs(readState()); !reflect.DeepEqual(ids, []string{"w2:zz"}) {
		t.Fatal(ids)
	}
	if _, ok := e.herdr.tokens("w2:zz")[mark]; !ok {
		t.Fatal("not hidden again under its new ID")
	}
}

func pick(target string, shown *agentID) error {
	_, err := snooze("pane", target, time.Now().Add(time.Hour), loadConfig(), shown, "")
	return err
}

func TestThePickerRefusesAPaneWhoseAgentChanged(t *testing.T) {
	e := cmdEnv(t)
	e.herdr.edit(func() { e.herdr.pane("zz")["agent"] = "codex" })
	if err := pick("zz", &agentID{"claude"}); !errors.Is(err, errStale) {
		t.Fatal(err)
	}
	if err := pick("gone", &agentID{"claude"}); !errors.Is(err, errStale) { // moved or closed since it opened
		t.Fatal(err)
	}
	e.herdr.edit(func() { e.herdr.pane("zz")["agent"] = nil })
	if err := pick("zz", &agentID{"claude"}); !errors.Is(err, errStale) { // exited since
		t.Fatal(err)
	}
	if len(readState().Panes) != 0 {
		t.Fatal(readState().Panes)
	}
}

func TestThePickerSnoozesTheAgentItShowed(t *testing.T) {
	cmdEnv(t)
	if err := pick("zz", &agentID{"claude"}); err != nil {
		t.Fatal(err)
	}
	e := readState().Panes["zz"]
	if e == nil || e.AgentID == nil || e.AgentID.Agent != "claude" || e.TerminalID != "t-zz" {
		t.Fatalf("%+v", e)
	}
}

// A pass of tick reads pane.list once: counting those counts passes.

func TestABurstOfTicksCoalesces(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()+hourMS)
	pending := filepath.Join(stateDir(), "tick.pending")
	unlock := lockFile(t, filepath.Join(stateDir(), "tick.lock")) // a tick is running
	mustRun(t, "tick")
	if calls := e.herdr.callList(); len(calls) != 0 {
		t.Fatal("didn't queue up behind the running tick:", calls)
	}
	if !exists(pending) {
		t.Fatal("no note left")
	}
	unlock()
	mustRun(t, "tick") // the note from the burst: one more pass
	if n := e.herdr.count("pane.list"); n != 1 || exists(pending) {
		t.Fatal(n, exists(pending))
	}
}

func TestALongBurstIsNotCutShort(t *testing.T) {
	// 0.2's first cut stopped after three passes, leaving the rest of a
	// burst to the 5-minute safety tick.
	e := cmdEnv(t)
	e.snoozed(t, nowMS()+hourMS)
	pending := filepath.Join(stateDir(), "tick.pending")
	passes := 0
	e.herdr.edit(func() {
		e.herdr.onCall = func(method string) {
			if method == "pane.list" {
				if passes++; passes < 5 {
					os.WriteFile(pending, nil, 0o600) // another hook arrived during this pass
				}
			}
		}
	})
	mustRun(t, "tick")
	if passes != 5 || exists(pending) {
		t.Fatal(passes, exists(pending))
	}
}

func TestADetectedHookMarksThePaneAndIsNeverCoalescedAway(t *testing.T) {
	// Its event is the only evidence of a same-kind replacement: a note for
	// the running tick would lose it.
	e := cmdEnv(t)
	e.write(t, fmt.Sprintf(`{"panes": {"zz": {"until": %d, "via": "pane", "since": 0, "agent_id": {"agent": "claude"}}}}`, nowMS()+hourMS))
	mustRun(t, "tick")
	if _, ok := e.herdr.tokens("zz")[mark]; !ok {
		t.Fatal("not hidden")
	}
	event, _ := json.Marshal(detectedHook("zz", "claude", true))
	t.Setenv("HERDR_PLUGIN_EVENT_JSON", string(event))
	unlock := lockFile(t, filepath.Join(stateDir(), "tick.lock")) // a plain tick is running
	defer unlock()
	mustRun(t, "tick", "--detected")
	if len(readState().Panes) != 0 {
		t.Fatal(readState().Panes)
	}
	if _, ok := e.herdr.tokens("zz")[mark]; ok {
		t.Fatal("the new agent doesn't show")
	}
}

// broken makes every herdr call fail as a dead connection.
func broken(e *env) {
	e.herdr.edit(func() { e.herdr.fail = func(string, map[string]any) (string, bool) { return "", true } })
}

func TestAFailedTimerTickArmsARetry(t *testing.T) {
	// The sleeper ticks once; if that tick fails and no hook follows, nothing
	// would arm the next and long snoozes lapse.
	e := cmdEnv(t)
	e.snoozed(t, nowMS()+2*24*hourMS)
	live := startSleeper(t)
	spawned := 0
	stubSpawn(t, func(int64) (int, error) { spawned++; return live.pid, nil })
	broken(e)
	for _, args := range [][]string{{"tick"}, {"tick", "--timer"}} {
		spawned = 0
		if _, err := run(args); err == nil {
			t.Fatal(args, "didn't fail")
		}
		if want := len(args) == 2; (spawned == 1) != want {
			t.Fatal(args, "spawned", spawned)
		}
	}
	// Recorded as the sleeper: the next tick counts on it instead of arming a
	// second one beside it.
	st := readState()
	if st.TimerPID == nil || *st.TimerPID != live.pid || *st.Timer <= nowMS() {
		t.Fatal(st.TimerPID, st.Timer)
	}
	spawned = 0
	armTimer(st, nowMS(), spawnTimer)
	if spawned != 0 {
		t.Fatal("armed a second sleeper")
	}
	e.herdr.stop() // herdr gone: don't keep a sleeper going
	if _, err := run([]string{"tick", "--timer"}); err == nil || spawned != 0 {
		t.Fatal(err, spawned)
	}
}

func TestAFailedTimerTickLeavesALiveSleeperArmedMeanwhile(t *testing.T) {
	// Another hook armed a sooner sleeper while this tick failed.
	e := cmdEnv(t)
	e.snoozed(t, nowMS()+2*24*hourMS)
	other := startSleeper(t)
	withState(func(st *state) error {
		at := nowMS() + 60_000
		st.Timer, st.TimerPID = &at, &other.pid
		return nil
	})
	spawned := 0
	stubSpawn(t, func(int64) (int, error) { spawned++; return 4545, nil })
	broken(e)
	if _, err := run([]string{"tick", "--timer"}); err == nil || spawned != 0 || *readState().TimerPID != other.pid {
		t.Fatal(err, spawned, *readState().TimerPID)
	}
	// This tick's own PID (the sleeper exec'd into it) is replaced.
	withState(func(st *state) error {
		self := os.Getpid()
		st.TimerPID = &self
		return nil
	})
	if _, err := run([]string{"tick", "--timer"}); err == nil || *readState().TimerPID != 4545 {
		t.Fatal(err, *readState().TimerPID)
	}
}

// ── the panel: toggle and order ──

func TestToggleThereAndBack(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()+hourMS)
	mustRun(t, "toggle")
	sameJSON(t, e.herdr.lastViewParams()["filter"], snoozedListFilter, "there")
	if from := readState().View.FromPane; from != "work" {
		t.Fatal("not where it was opened from:", from)
	}
	mustRun(t, "toggle")
	sameJSON(t, e.herdr.lastViewParams()["filter"], viewFilter, "back")
	if n := e.herdr.notifications(); len(n) != 0 {
		t.Fatal(n)
	}
}

func TestToggleWalksBothOrdersAndTheSnoozedList(t *testing.T) {
	e := freeEnv(t)
	e.snoozed(t, nowMS()+hourMS)
	mustRun(t, "tick") // herdr's own here is spaces (an empty home)
	if l := e.herdr.lastViewParams()["label"]; l != "grouped z 1" {
		t.Fatal(l)
	}
	var seen []string
	for range 6 {
		mustRun(t, "toggle")
		p := e.herdr.lastViewParams()
		seen = append(seen, fmt.Sprintf("%v|%v", p["label"], reflect.DeepEqual(norm(p["filter"]), norm(snoozedListFilter))))
	}
	lap := []string{"z snoozed only · 1|true", "priority* z 1|false", "grouped z 1|false"}
	if want := append(lap, lap...); !reflect.DeepEqual(seen, want) {
		t.Fatalf("%q", seen)
	}
	sameJSON(t, e.herdr.lastViewParams()["sort"], panelModes["spaces"].sort, "sort")
	if n := e.herdr.notifications(); len(n) != 0 {
		t.Fatal(n)
	}
}

func TestToggleSkipsTheOrdersWhenTheyAreNotOurs(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()+hourMS)
	mustRun(t, "tick") // borrows the radar's view
	for _, snoozed := range []bool{true, false, true, false} {
		mustRun(t, "toggle")
		p := e.herdr.lastViewParams()
		if reflect.DeepEqual(norm(p["filter"]), norm(snoozedListFilter)) != snoozed {
			t.Fatal(p)
		}
		sameJSON(t, p["sort"], knownViews[radar].sorts["active"], "sort")
	}
}

func TestOrderFlipsHerdrsOwnOrderUntilTheLastSnoozeEnds(t *testing.T) {
	e := freeEnv(t)
	e.snoozed(t, nowMS()+hourMS)
	mustRun(t, "order") // herdr's own here is spaces (an empty home)
	p := e.herdr.lastViewParams()
	if p["label"] != "priority* z 1" {
		t.Fatal(p["label"])
	}
	sameJSON(t, p["sort"], panelModes["priority"].sort, "sort")
	mustRun(t, "tick")
	if l := e.herdr.lastViewParams()["label"]; l != "priority* z 1" {
		t.Fatal("a tick didn't keep it:", l)
	}
	mustRun(t, "order")
	if l := e.herdr.lastViewParams()["label"]; l != "grouped z 1" || readState().View.Order != "" {
		t.Fatal(l, readState().View)
	}
	mustRun(t, "order")
	mustRun(t, "wake", "--all") // the flip ends with the view
	sameJSON(t, e.herdr.currentView(), map[string]any{"active": false}, "view")
	if n := e.herdr.notifications(); len(n) != 0 {
		t.Fatal(n)
	}
}

func TestOrderLeavesABorrowedOrderAlone(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()+hourMS)
	mustRun(t, "tick")
	mustRun(t, "order")
	if n := e.herdr.notifications(); !reflect.DeepEqual(n, []string{"Snooze: The panel's order belongs to " + radar + "."}) {
		t.Fatal(n)
	}
	sameJSON(t, e.herdr.lastViewParams()["sort"], knownViews[radar].sorts["active"], "sort")
}

func onlyToasts(t *testing.T, e *env) {
	t.Helper()
	for _, c := range e.herdr.callList() {
		if c != "notification.show" {
			t.Fatal("touched herdr:", e.herdr.callList())
		}
	}
}

func TestOrderWithNothingSnoozedPointsAtHerdrsOwnSwitch(t *testing.T) {
	e := cmdEnv(t)
	mustRun(t, "order")
	if n := e.herdr.notifications(); !reflect.DeepEqual(n, []string{"Snooze: Nothing is snoozed: click the panel's label to switch its order."}) {
		t.Fatal(n)
	}
	onlyToasts(t, e)
}

// Unlike 0.2, a toggle with nothing snoozed no longer records herdr's
// order it stepped to: it writes no state file at all.
func TestToggleWithNothingSnoozedSaysSoAndTouchesNothing(t *testing.T) {
	e := cmdEnv(t)
	mustRun(t, "toggle")
	if n := e.herdr.notifications(); !reflect.DeepEqual(n, []string{"Snooze: Nothing is snoozed."}) {
		t.Fatal(n)
	}
	onlyToasts(t, e)
	if exists(e.file()) {
		t.Fatal("left a state file")
	}
}

func TestToggleOnAStaleFileExplainsItself(t *testing.T) {
	// the only deadline passed while no hook ran: this sync clears it, so the
	// key press would otherwise do nothing, silently
	e := cmdEnv(t)
	e.snoozed(t, nowMS()-1000)
	mustRun(t, "toggle")
	// what woke says why: herdr would drop a second toast this close
	if n := e.herdr.notifications(); !reflect.DeepEqual(n, []string{"claude is awake: claude · One"}) {
		t.Fatal(n)
	}
	sameJSON(t, e.herdr.currentView(), map[string]any{"active": true, "source": radar, "label": "active"}, "view")
	if !onlyJustWoke(readState()) {
		t.Fatal("more than the just-woke list left")
	}
}

// ── damage ──

func TestDamagedFileFailsOpenThenGoes(t *testing.T) {
	// A record we can't read may stand for a live token and our live view.
	// Deleting the file unexamined could leave agents hidden with nothing to
	// un-hide them. Instead: clear tokens with no readable record and our
	// view, then remove the file.
	for _, bad := range []string{`{"panes": {"zz": {"until": "soon"}}}`, "{not json", "[]"} {
		t.Run(bad, func(t *testing.T) {
			e := cmdEnv(t)
			e.herdr.edit(func() {
				e.herdr.pane("zz")["tokens"] = map[string]any{token: "z 12:00"}
				e.herdr.view = map[string]any{"active": true, "source": source(), "label": "z 1"}
			})
			e.write(t, bad)
			mustRun(t, "tick")
			if _, ok := e.herdr.tokens("zz")[token]; ok {
				t.Fatal("token left")
			}
			sameJSON(t, e.herdr.currentView(), map[string]any{"active": false}, "view")
			if exists(e.file()) {
				t.Fatal("file left: every hook does work, forever")
			}
		})
	}
}

func TestMissingOrWrongViewCountsAsDamaged(t *testing.T) {
	for _, bad := range []string{`{"view": "x"}`, `{"v": 1, "panes": {}, "workspaces": {}, "view": null}`, `{"v": 1}`} {
		t.Run(bad, func(t *testing.T) {
			e := cmdEnv(t)
			e.herdr.edit(func() { e.herdr.view = map[string]any{"active": true, "source": source(), "label": "z 1"} })
			e.write(t, bad)
			mustRun(t, "tick")
			sameJSON(t, e.herdr.currentView(), map[string]any{"active": false}, "our filter left live")
			if exists(e.file()) {
				t.Fatal("file left")
			}
		})
	}
}

func TestDamagedFileLeavesAnotherPluginsViewAlone(t *testing.T) {
	e := cmdEnv(t)
	e.write(t, "{not json")
	mustRun(t, "tick")
	sameJSON(t, e.herdr.currentView(), map[string]any{"active": true, "source": radar, "label": "active"}, "view")
	if exists(e.file()) {
		t.Fatal("file left")
	}
}

func TestFailedLastClearKeepsTheFileSoTheNextTickRetries(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()-1000) // the last snooze has ended; clearing our view is all that's left
	e.herdr.edit(func() {
		e.herdr.view = map[string]any{"active": true, "source": source(), "label": "z 1"}
		e.herdr.fail = func(method string, params map[string]any) (string, bool) {
			return "", method == "agent.view.clear" && params["source"] == source() // herdr went away mid-call
		}
	})
	if _, err := run([]string{"tick"}); err == nil {
		t.Fatal("didn't fail")
	}
	if !exists(e.file()) {
		t.Fatal("deleted with our filter still live: nothing would retry")
	}
	e.herdr.edit(func() { e.herdr.fail = nil }) // herdr is back
	mustRun(t, "tick")
	sameJSON(t, e.herdr.currentView(), map[string]any{"active": false}, "view")
	if !onlyJustWoke(readState()) {
		t.Fatal("more than the just-woke list left")
	}
}

func TestListSaysWhatIsSnoozed(t *testing.T) {
	e := cmdEnv(t)
	e.write(t, fmt.Sprintf(`{"panes": {"zz": {"until": %d, "via": "pane"}}, "workspaces": {"w2": {"until": %d}}, "view": {}}`,
		nowMS()+hourMS, nowMS()+2*hourMS))
	out := captureStdout(t, printList)
	if !strings.Contains(out, "workspace:w2\t2h left") || !strings.Contains(out, "zz\t1h left") || !strings.Contains(out, "(pane)") {
		t.Fatal(out)
	}
}
