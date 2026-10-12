package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Upgrading from 0.1.0: one state file for every session, at the top of
// the plugin's state directory.

func legacyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(pluginStateDir(), "snoozed.json")
	text := fmt.Sprintf(`{"panes": {"zz": {"until": %d, "via": "pane"}}, "workspaces": {}, "view": {}}`, nowMS()+hourMS)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// secondSession is a second running herdr session with its own herdr:
// quiet (it never ticks here), still showing 0.1.0's snoozed-only view, with
// a 0.1.0 token on its own w1-numbered pane. Not listening until reachable.
func secondSession(t *testing.T, e *env, reachable bool) *fakeHerdr {
	t.Helper()
	sock := filepath.Join(e.dir, "herdr-b", "herdr.sock")
	b := &fakeHerdr{
		panes: []map[string]any{fpane("zz", map[string]any{"tokens": map[string]any{token: "z 12:00"}})},
		view:  map[string]any{"active": true, "source": source(), "label": "z snoozed only · 1"},
		sock:  sock,
	}
	os.MkdirAll(filepath.Dir(sock), 0o700)
	if reachable {
		b.listen(t)
	}
	e.setSessions(e.sock, sock)
	return b
}

func pastTheBackoff(t *testing.T) {
	t.Helper()
	path := filepath.Join(pluginStateDir(), "legacy-retry.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var retry map[string]any
	json.Unmarshal(data, &retry)
	retry["at"] = 0
	data, _ = json.Marshal(retry)
	os.WriteFile(path, data, 0o600)
}

func retryDelay(t *testing.T) float64 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(pluginStateDir(), "legacy-retry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var retry struct{ Delay float64 }
	json.Unmarshal(data, &retry)
	return retry.Delay
}

func TestLegacyStateIsAdoptedByNoSessionAndFailsOpenEverywhere(t *testing.T) {
	e := cmdEnv(t)
	legacy := legacyFile(t)
	b := secondSession(t, e, true)
	e.herdr.edit(func() {
		e.herdr.pane("zz")["tokens"] = map[string]any{token: "z 12:00"} // 0.1.0's token, maybe this session's
		e.herdr.view = map[string]any{"active": true, "source": source(), "label": "z 1"}
	})
	mustRun(t, "tick")
	// this session: the agent reappears, and "zz" wasn't adopted as ours
	if _, ok := e.herdr.tokens("zz")[token]; ok {
		t.Fatal("token left")
	}
	sameJSON(t, e.herdr.currentView(), map[string]any{"active": false}, "view")
	if len(readState().Panes) != 0 {
		t.Fatal(readState().Panes)
	}
	// the quiet session, which never ticked, recovered through its own socket
	sameJSON(t, b.currentView(), map[string]any{"active": false}, "b's view")
	if _, ok := b.tokens("zz")[token]; ok {
		t.Fatal("b's token left")
	}
	// everyone recovered: the old file goes, and hooks go idle again
	if exists(legacy) {
		t.Fatal("legacy file left")
	}
}

func TestLegacyFileStaysUntilEveryRunningSessionRecovered(t *testing.T) {
	e := cmdEnv(t)
	legacy := legacyFile(t)
	b := secondSession(t, e, false)
	mustRun(t, "tick")
	if !exists(legacy) {
		t.Fatal("removed with a session still unrecovered")
	}
	e.herdr.clearCalls()
	b.listen(t)
	pastTheBackoff(t)
	mustRun(t, "tick")
	sameJSON(t, b.currentView(), map[string]any{"active": false}, "b's view")
	if exists(legacy) {
		t.Fatal("legacy file left")
	}
	if n := e.herdr.count("agent.view.clear"); n != 0 {
		t.Fatal("recovered this session again")
	}
}

func TestARefusingSessionIsRetriedWithBackoffNotOnEveryHook(t *testing.T) {
	e := cmdEnv(t)
	legacyFile(t)
	secondSession(t, e, false)
	mustRun(t, "tick")
	mustRun(t, "tick") // straight after: backing off
	if n := e.timesListed(); n != 1 {
		t.Fatal("every hook paid for another recovery attempt:", n)
	}
	first := retryDelay(t)
	pastTheBackoff(t)
	mustRun(t, "tick") // fails again: waits longer
	if d := retryDelay(t); d != first*2 {
		t.Fatal(first, d)
	}
}

func TestOnlyOneProcessRecoversAtATime(t *testing.T) {
	e := cmdEnv(t)
	legacyFile(t)
	f, _ := os.OpenFile(filepath.Join(pluginStateDir(), "legacy.lock"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	defer f.Close()
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX) // another process is at it
	mustRun(t, "tick")
	if e.timesListed() != 0 {
		t.Fatal("recovered alongside it")
	}
}

func TestARefusedTokenClearIsNotCountedAsRecovered(t *testing.T) {
	// The clear failed, yet the session was marked done and the 0.1.0 file
	// removed, leaving that agent hidden.
	e := cmdEnv(t)
	legacy := legacyFile(t)
	e.herdr.edit(func() {
		e.herdr.pane("zz")["tokens"] = map[string]any{token: "z 12:00"}
		e.herdr.fail = func(method string, params map[string]any) (string, bool) {
			if tokens, ok := params["tokens"].(map[string]any); method == "pane.report_metadata" && ok {
				if v, has := tokens[token]; has && v == nil {
					return "denied", false
				}
			}
			return "", false
		}
	})
	run([]string{"tick"})
	if !exists(legacy) || exists(filepath.Join(stateDir(), "legacy-recovered")) {
		t.Fatal("counted as recovered")
	}
}

func TestLegacyFileStaysIfHerdrCantListSessions(t *testing.T) {
	e := cmdEnv(t)
	legacy := legacyFile(t)
	os.WriteFile(e.failList, nil, 0o600)
	mustRun(t, "tick")
	if !exists(legacy) {
		t.Fatal("no proof the others recovered, yet removed")
	}
}

func TestASessionWithItsOwnSnoozesKeepsThemThroughRecovery(t *testing.T) {
	e := cmdEnv(t)
	e.snoozed(t, nowMS()+hourMS)
	mustRun(t, "tick") // takes the view for "zz"
	legacyFile(t)
	mustRun(t, "tick")
	if readState().Panes["zz"] == nil {
		t.Fatal("lost its own snooze")
	}
	if v := e.herdr.currentView(); v["source"] != source() {
		t.Fatal(v)
	}
}
