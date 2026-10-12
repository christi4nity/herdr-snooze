package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"time"
)

// recapPlugin is herdr-recap: the selected agent's recap is a key away, when
// it's installed.
const recapPlugin = "herdr-recap"

// returnEnv is how a popup is told where it was opened from: a popupRef, as
// JSON, to open again when it's closed. herdr shows one popup at a time, so
// going from one plugin's to another's means closing the first.
const returnEnv = "HERDR_POPUP_RETURN"

// popupRef is a plugin's popup, as plugin.pane.open takes it.
type popupRef struct {
	PluginID   string            `json:"plugin_id"`
	Entrypoint string            `json:"entrypoint"`
	Width      string            `json:"width,omitempty"`
	Height     string            `json:"height,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

// A plugin's or an entrypoint's ID, as herdr allows them: what a popupRef
// read from the environment is held to.
var refID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (p popupRef) valid() bool {
	return refID.MatchString(p.PluginID) && refID.MatchString(p.Entrypoint)
}

// siblings are the other plugins installed and enabled, each with its
// popup: the ones a row can link to. A variable so tests can stand one in.
var siblings = func() map[string]popupRef {
	var res struct {
		Plugins []struct {
			PluginID string `json:"plugin_id"`
			Enabled  bool   `json:"enabled"`
			Panes    []struct {
				ID        string `json:"id"`
				Placement string `json:"placement"`
				Width     string `json:"width"`
				Height    string `json:"height"`
			} `json:"panes"`
		} `json:"plugins"`
	}
	if herdrCall("plugin.list", nil, &res) != nil {
		return nil
	}
	out := map[string]popupRef{}
	for _, p := range res.Plugins {
		if !p.Enabled || p.PluginID == pluginID() {
			continue
		}
		for _, pane := range p.Panes {
			if pane.ID == "picker" && pane.Placement == "popup" {
				out[p.PluginID] = popupRef{PluginID: p.PluginID, Entrypoint: pane.ID, Width: pane.Width, Height: pane.Height}
			}
		}
	}
	return out
}

// handOver closes this popup for another: it's what this program becomes
// once the popup's own terminal is restored (swap.go).
func handOver(p popupRef) error {
	if !p.valid() {
		return errors.New("not a popup: " + p.PluginID + "/" + p.Entrypoint)
	}
	pendingSwap = &p
	return nil
}

// handoffWait is how long a popup is given to close before the next one's
// open is given up on.
const handoffWait = 4 * time.Second

// handOverDetached opens another popup once this one has closed: by this
// program again, detached, since the popup's own process ends with it. For
// when becoming it (swap.go) can't be done.
func handOverDetached(p popupRef) error {
	if !p.valid() {
		return errors.New("not a popup: " + p.PluginID + "/" + p.Entrypoint)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "handoff", string(data))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// runHandoff is the detached half of handOverDetached: it opens the popup as
// soon as herdr has room for it.
func runHandoff(arg string) error {
	var p popupRef
	if err := json.Unmarshal([]byte(arg), &p); err != nil {
		return err
	}
	if !p.valid() {
		return errors.New("not a popup: " + p.PluginID + "/" + p.Entrypoint)
	}
	params := map[string]any{
		"plugin_id": p.PluginID, "entrypoint": p.Entrypoint, "placement": "popup", "focus": true, "env": p.Env,
	}
	if p.Width != "" && p.Height != "" {
		params["width"], params["height"] = sizeParam(p.Width), sizeParam(p.Height)
	}
	var err error
	for deadline := time.Now().Add(handoffWait); ; time.Sleep(60 * time.Millisecond) {
		if err = herdrCall("plugin.pane.open", params, nil); err == nil || !isHerdrCode(err, "ui_busy") || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		notify("Snooze", "Couldn't open "+p.PluginID+": "+err.Error())
	}
	return err
}

// listHere is this popup's list, to come back to with pane selected.
func listHere(pane string) popupRef {
	return popupRef{PluginID: pluginID(), Entrypoint: "picker", Width: listWidth, Height: listHeight,
		Env: map[string]string{envScreen: "list", envPane: pane}}
}

// recapFor is herdr-recap's popup on an agent, with this popup's list to
// come back to.
func recapFor(to popupRef, pane string) popupRef {
	back, _ := json.Marshal(listHere(pane))
	to.Env = map[string]string{"HERDR_RECAP_PANE": pane, returnEnv: string(back)}
	return to
}

// cameFrom is the popup this one was opened from, if it was.
func cameFrom() (popupRef, bool) {
	var p popupRef
	if json.Unmarshal([]byte(os.Getenv(returnEnv)), &p) != nil || !p.valid() {
		return popupRef{}, false
	}
	return p, true
}
