package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// pythonState is a state file as snooze 0.2 (Python) wrote it:
// json.dump(state, indent=1, sort_keys=True).
const pythonState = `{
 "panes": {
  "w1:p1": {
   "agent_id": {
    "agent": "claude"
   },
   "dirty": true,
   "since": 1789990000000,
   "terminal_id": "term_1",
   "token_until": 1790000000000,
   "until": 1790000000000,
   "via": "pane",
   "workspace_id": "w1"
  },
  "w2:p3": {
   "agent_gone": true,
   "agent_id": {
    "agent": "codex"
   },
   "token_until": 1790003600000,
   "until": 1790003600000,
   "via": "workspace",
   "workspace_id": "w2"
  }
 },
 "timer": 1789999000000,
 "timer_pid": 4242,
 "v": 1,
 "view": {
  "applied": "{\"label\": \"z 2\"}",
  "from_pane": null,
  "inherited": {
   "label": "active",
   "source": "plugin:hhdebb.herdr-radar"
  },
  "order": "priority",
  "showing": "snoozed"
 },
 "workspaces": {
  "w2": {
   "except": [
    "w2:p9"
   ],
   "except_terminals": {
    "w2:p9": "term_9"
   },
   "token_until": 1790003600000,
   "until": 1790003600000
  }
 }
}`

func stateHere(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SNOOZE_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(dir, "herdr", "herdr.sock"))
	os.MkdirAll(filepath.Join(dir, "herdr"), 0o700)
	os.MkdirAll(stateDir(), 0o700)
}

func TestReadsAStateFilePythonWrote(t *testing.T) {
	stateHere(t)
	os.WriteFile(stateFile(), []byte(pythonState), 0o600)
	st := readState()
	want := map[string]*paneEntry{
		"w1:p1": {Until: 1790000000000, Via: viaPane, WorkspaceID: "w1", TerminalID: "term_1", AgentID: &agentID{"claude"},
			Since: 1789990000000, TokenUntil: 1790000000000, Dirty: true},
		"w2:p3": {Until: 1790003600000, Via: viaWorkspace, WorkspaceID: "w2", AgentID: &agentID{"codex"},
			TokenUntil: 1790003600000, AgentGone: true},
	}
	if !reflect.DeepEqual(st.Panes, want) {
		t.Fatalf("%+v", st.Panes)
	}
	space := st.Workspaces["w2"]
	if space == nil || space.Until != 1790003600000 || !reflect.DeepEqual(space.Except, []string{"w2:p9"}) ||
		space.ExceptTerminals["w2:p9"] != "term_9" {
		t.Fatalf("%+v", space)
	}
	wantView := viewRecord{Applied: `{"label": "z 2"}`, Inherited: &inheritedView{Source: radar, Label: "active"},
		Showing: showingSnoozed, Order: "priority"}
	if !reflect.DeepEqual(st.View, wantView) {
		t.Fatalf("%+v", st.View)
	}
	if st.Timer == nil || *st.Timer != 1789999000000 || st.TimerPID == nil || *st.TimerPID != 4242 {
		t.Fatal(st.Timer, st.TimerPID)
	}
}

func TestWhatGoWritesItReadsBack(t *testing.T) {
	stateHere(t)
	os.WriteFile(stateFile(), []byte(pythonState), 0o600)
	before := readState()
	if err := withState(func(st *state) error { st.Panes["w1:p1"].Until++; return nil }); err != nil {
		t.Fatal(err)
	}
	after := readState()
	before.Panes["w1:p1"].Until++
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%+v\n%+v", before, after)
	}
}

func TestAMalformedEntryIsDroppedAndTheRestKept(t *testing.T) {
	stateHere(t)
	os.WriteFile(stateFile(), []byte(`{"panes": {"a": {"until": 5}, "b": {"until": 1.5}, "c": {"until": 5, "via": 3}, "d": []},
		"workspaces": {}, "view": {}}`), 0o600)
	st := readState()
	if !reflect.DeepEqual(paneIDs(st), []string{"a"}) {
		t.Fatal(paneIDs(st))
	}
	if st.View != recoverView() {
		t.Fatal("damage not marked for recovery:", st.View)
	}
}

func TestNoFileIsNothingSnoozed(t *testing.T) {
	stateHere(t)
	if st := readState(); !st.idle() {
		t.Fatalf("%+v", st)
	}
}

func TestAnIdleStateLeavesNoFile(t *testing.T) {
	stateHere(t)
	withState(func(st *state) error { st.Panes["p"] = &paneEntry{Until: 1}; return nil })
	if !exists(stateFile()) {
		t.Fatal("not written")
	}
	withState(func(st *state) error { delete(st.Panes, "p"); return nil })
	if exists(stateFile()) {
		t.Fatal("left behind: every hook does work")
	}
}
