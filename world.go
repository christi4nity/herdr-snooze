package main

import "time"

// world is what the popup reads and changes: herdr and the state file, or
// the demo's made-up agents.
type world interface {
	load() loadedMsg
	snooze(t target, until time.Time, cfg config) error
	wake(cfg config, panes, spaces []string, all bool) error
	focus(pane string) error
	// open hands the popup over to another plugin's (links.go).
	open(p popupRef) error
	siblings() map[string]popupRef
}

type liveWorld struct{}

func (liveWorld) load() loadedMsg {
	panes, err := listPanes()
	spaces, _ := listWorkspaces()
	return loadedMsg{st: readState(), panes: panes, spaces: spaces, err: err}
}

func (liveWorld) snooze(t target, until time.Time, cfg config) error {
	covered, err := snooze(t.Kind, t.ID, until, cfg, t.Agent)
	if err == nil {
		logf("snoozed %s %s (%d pane(s)) until %s", t.Kind, t.ID, covered, until.Format("2006-01-02 15:04"))
	}
	return err
}

func (liveWorld) wake(cfg config, panes, spaces []string, all bool) error {
	woken, err := wake(cfg, panes, spaces, all)
	if err == nil {
		logf("woke %d pane(s)", woken)
	}
	return err
}

func (liveWorld) focus(pane string) error       { return focusAgent(pane) }
func (liveWorld) open(p popupRef) error         { return handOver(p) }
func (liveWorld) siblings() map[string]popupRef { return siblings() }
