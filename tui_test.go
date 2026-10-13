package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// openWorld is the demo, remembering the popups it was asked to open.
type openWorld struct {
	*demoWorld
	opened []popupRef
}

func (w *openWorld) open(p popupRef) error { w.opened = append(w.opened, p); return nil }

// runCmd carries a command out and feeds its message back, as the program
// would: one level, which is all the popup's commands need.
func runCmd(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if _, quit := msg.(tea.QuitMsg); quit {
		return m
	}
	next, _ := m.Update(msg)
	return next.(model)
}

func key(t *testing.T, m model, k string) (model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(keyMsg(k))
	return next.(model), cmd
}

func stopClock(t *testing.T) {
	stopped := time.Date(2026, 10, 13, 14, 5, 0, 0, time.Local)
	old := timeNow
	timeNow = func() time.Time { return stopped }
	t.Cleanup(func() { timeNow = old })
}

func TestOpenedOnAnAwakeAgentOffersTheDurations(t *testing.T) {
	stopClock(t)
	m := demoScreen(newModel(defaultConfig(), newDemoWorld(), target{}, false, "w2:p1", nil), 52, 18)
	if m.screen != screenPick || m.target.ID != "w2:p1" || m.target.Snoozed {
		t.Fatalf("screen %v target %+v", m.screen, m.target)
	}
	if strings.Contains(m.View(), "w wake") {
		t.Error("an awake agent is offered a wake")
	}
}

func TestOpenedOnASnoozedAgentOffersAChangeAndAWake(t *testing.T) {
	stopClock(t)
	w := &openWorld{demoWorld: newDemoWorld()}
	m := demoScreen(newModel(defaultConfig(), w, target{}, false, "w3:p1", nil), 52, 18)
	if !m.target.Snoozed || !strings.Contains(m.target.Detail, "now until 16:19") {
		t.Fatalf("target %+v", m.target)
	}
	if view := m.View(); !strings.Contains(view, "w wake") || !strings.Contains(view, "enter change") {
		t.Errorf("keys missing:\n%s", view)
	}
	m, cmd := key(t, m, "w")
	runCmd(t, m, cmd)
	if w.st.Panes["w3:p1"] != nil {
		t.Error("w didn't wake it")
	}
}

func TestOpenedFromAnotherPopupGoesBackAfterSnoozing(t *testing.T) {
	stopClock(t)
	back := popupRef{PluginID: "herdr-recap", Entrypoint: "picker", Env: map[string]string{"HERDR_RECAP_PANE": "w2:p1"}}
	w := &openWorld{demoWorld: newDemoWorld()}
	m := demoScreen(newModel(defaultConfig(), w, target{}, false, "w2:p1", &back), 52, 18)
	if !strings.Contains(m.View(), "esc back") {
		t.Error("esc doesn't say it goes back")
	}
	m, cmd := key(t, m, "1")
	m = runCmd(t, m, cmd)
	if w.st.Panes["w2:p1"] == nil {
		t.Fatal("not snoozed")
	}
	if len(w.opened) != 1 || w.opened[0].PluginID != "herdr-recap" || w.opened[0].Env["HERDR_RECAP_PANE"] != "w2:p1" {
		t.Errorf("went back to %+v", w.opened)
	}
}

func TestEscGoesBackToTheOpener(t *testing.T) {
	back := popupRef{PluginID: "herdr-recap", Entrypoint: "picker"}
	w := &openWorld{demoWorld: newDemoWorld()}
	m := demoScreen(newModel(defaultConfig(), w, target{}, false, "w2:p1", &back), 52, 18)
	if _, cmd := key(t, m, "esc"); cmd == nil || len(w.opened) != 1 {
		t.Errorf("esc: opened %+v", w.opened)
	}
}

func TestAPaneThatIsGoneSaysSo(t *testing.T) {
	m := demoScreen(newModel(defaultConfig(), newDemoWorld(), target{}, false, "w9:p9", nil), 52, 18)
	if m.screen != screenList || !strings.Contains(m.err, "isn't there") {
		t.Errorf("screen %v err %q", m.screen, m.err)
	}
}

func TestTheListOpensRecapOnTheSelectedAgentAndComesBack(t *testing.T) {
	w := &openWorld{demoWorld: newDemoWorld()}
	m := demoScreen(newModel(defaultConfig(), w, target{}, true, "w1:p1", nil), 96, 24)
	if r := m.selected(); r == nil || r.id != "w1:p1" {
		t.Fatalf("selected %+v", r)
	}
	if !strings.Contains(m.View(), "r recap") {
		t.Error("no recap key with recap installed")
	}
	key(t, m, "r")
	if len(w.opened) != 1 || w.opened[0].Env["HERDR_RECAP_PANE"] != "w1:p1" {
		t.Fatalf("opened %+v", w.opened)
	}
	var back popupRef
	if err := json.Unmarshal([]byte(w.opened[0].Env[returnEnv]), &back); err != nil {
		t.Fatal(err)
	}
	if back.PluginID != pluginID() || back.Env[envScreen] != "list" || back.Env[envPane] != "w1:p1" {
		t.Errorf("comes back to %+v", back)
	}
}

func TestNoRecapKeyWithoutRecap(t *testing.T) {
	m := demoScreen(newModel(defaultConfig(), newDemoWorld(), target{}, true, "", nil), 96, 24)
	m.links = map[string]popupRef{}
	if strings.Contains(m.View(), "r recap") {
		t.Error("recap key without recap")
	}
}

func TestTheListKeepsASpacesAgentsUnderIt(t *testing.T) {
	m := demoScreen(newModel(defaultConfig(), newDemoWorld(), target{}, true, "", nil), 96, 24)
	var order []string
	for _, r := range m.rows {
		order = append(order, r.id)
	}
	got := strings.Join(order, " ")
	if !strings.HasPrefix(got, "Snoozed w4 w4:p1 w4:p2 w3:p1 w2:p2 w3:p3  Awake") {
		t.Errorf("rows: %s", got)
	}
}

func TestSnoozingFromTheListStaysInTheList(t *testing.T) {
	w := &openWorld{demoWorld: newDemoWorld()}
	m := demoScreen(newModel(defaultConfig(), w, target{}, true, "w1:p2", nil), 96, 24)
	m, _ = key(t, m, "s")
	if m.screen != screenPick || !m.fromList {
		t.Fatalf("screen %v", m.screen)
	}
	m, cmd := key(t, m, "2")
	m = runCmd(t, m, cmd)
	if m.screen != screenList || w.st.Panes["w1:p2"] == nil || !strings.HasPrefix(m.flash, "Snoozed") {
		t.Errorf("screen %v flash %q", m.screen, m.flash)
	}
}

func TestSwappingAwayDropsSnoozesOwnVariables(t *testing.T) {
	env := swapEnv([]string{"HERDR_SNOOZE_PANE=w1:p1", "HERDR_SNOOZE_SCREEN=list", "PATH=/bin"}, "herdr-snooze",
		popupRef{PluginID: "herdr-recap", Entrypoint: "picker"}, "/r")
	for _, kv := range env {
		if strings.HasPrefix(kv, "HERDR_SNOOZE_") {
			t.Errorf("kept %s", kv)
		}
	}
}

func TestWakeAndSnoozeAreSeparateKeys(t *testing.T) {
	stopClock(t)
	w := &openWorld{demoWorld: newDemoWorld()}
	// w on an awake agent does nothing: it's for waking.
	m := demoScreen(newModel(defaultConfig(), w, target{}, true, "w1:p2", nil), 96, 24)
	if m, cmd := key(t, m, "w"); cmd != nil || m.screen != screenList || w.st.Panes["w1:p2"] != nil {
		t.Fatal("w snoozed or did something to an awake agent")
	}
	if v := m.View(); strings.Contains(v, "w wake ") || !strings.Contains(v, "S snooze space") {
		t.Errorf("awake keys:\n%s", v)
	}
	// s on a snoozed one gives it a new time.
	m = demoScreen(newModel(defaultConfig(), w, target{}, true, "w3:p1", nil), 96, 24)
	if !strings.Contains(m.View(), "w wake") {
		t.Error("no w on a snoozed agent")
	}
	m, _ = key(t, m, "s")
	if m.screen != screenPick || !m.target.Snoozed {
		t.Fatalf("s on a snoozed agent: screen %v target %+v", m.screen, m.target)
	}
	m, cmd := key(t, m, "7") // a week
	m = runCmd(t, m, cmd)
	if e := w.st.Panes["w3:p1"]; e == nil || fromMS(e.Until).Sub(timeNow()) < 6*24*time.Hour {
		t.Errorf("not given a week: %+v", e)
	}
	// S snoozes its whole space.
	m = demoScreen(newModel(defaultConfig(), w, target{}, true, "w1:p1", nil), 96, 24)
	m, _ = key(t, m, "S")
	if m.screen != screenPick || m.target.Kind != "workspace" || m.target.ID != "w1" {
		t.Errorf("S: screen %v target %+v", m.screen, m.target)
	}
}
