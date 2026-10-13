package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// timeNow is the clock. A variable so tests can stop it.
var timeNow = time.Now

// The popup's environment. Another plugin opens it on an agent with
// envPane alone (and returnEnv, to go back); snooze's own actions say all of
// what they found, so the popup needn't ask herdr again.
const (
	envPane   = "HERDR_SNOOZE_PANE"   // the agent to open on
	envScreen = "HERDR_SNOOZE_SCREEN" // "list" opens the list of every agent
	envKind   = "HERDR_SNOOZE_KIND"   // "pane" or "workspace"
	envTarget = "HERDR_SNOOZE_TARGET" // its ID
	envLabel  = "HERDR_SNOOZE_LABEL"  // what it's called
	envDetail = "HERDR_SNOOZE_DETAIL" // the line under that
	envAgent  = "HERDR_SNOOZE_AGENT"  // the agent shown, as JSON
	envStatus = "HERDR_SNOOZE_STATUS" // its status, for its mark
	envDemo   = "HERDR_SNOOZE_DEMO"   // "1": fictional agents
	envPR     = "HERDR_SNOOZE_PR"     // its open pull request
)

// The popups' sizes: the duration picker is just big enough for its rows;
// the list has room for every agent.
const (
	listWidth, listHeight = "72%", "70%"
	pickerWidth           = 52
)

func pickerHeight(cfg config) int {
	// durations + Custom + until done and until merged + chrome, and never
	// less than Custom needs
	return max(min(len(cfg.Durations), 9)+12, 13)
}

type invocation struct {
	Source         string `json:"invocation_source"`
	FocusedPane    string `json:"focused_pane_id"`
	WorkspaceID    string `json:"workspace_id"`
	WorkspaceLabel string `json:"workspace_label"`
}

func invocationContext() invocation {
	var c invocation
	_ = json.Unmarshal([]byte(os.Getenv("HERDR_PLUGIN_CONTEXT_JSON")), &c)
	return c
}

// paneLabel is what a pane is called: its title, else its terminal's, else
// its agent.
func paneLabel(p pane) string {
	for _, s := range []string{p.Title, p.TitleStripped, p.TerminalTitle, p.Agent} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return p.PaneID
}

func runAction(name string) (int, error) {
	ctx := invocationContext()
	logf("action %s via %s pane %s workspace %s", name, ctx.Source, ctx.FocusedPane, ctx.WorkspaceID)
	cfg := loadConfig()
	paneID := ctx.FocusedPane
	if paneID == "" {
		paneID = os.Getenv("HERDR_PANE_ID")
	}

	switch name {
	case "wake":
		// The one-step way out, for the snoozed list: no popup to get through.
		if readState().Panes[paneID] == nil {
			notify("Snooze", "This agent isn't snoozed.")
			return 0, nil
		}
		_, err := wake(cfg, []string{paneID}, nil, false)
		return 0, err

	case "manage", "demo":
		env := map[string]string{envScreen: "list"}
		if name == "demo" {
			env[envDemo] = "1"
		}
		return openPicker(env, listWidth, listHeight)

	case "workspace":
		wid := ctx.WorkspaceID
		if wid == "" {
			wid = os.Getenv("HERDR_WORKSPACE_ID")
		}
		if readState().Workspaces[wid] != nil {
			_, err := wake(cfg, nil, []string{wid}, false) // the same action wakes it: one key, both ways
			return 0, err
		}
		spaces, err := listWorkspaces()
		if err != nil {
			return 1, err
		}
		var space *workspaceInfo
		for i := range spaces {
			if spaces[i].WorkspaceID == wid {
				space = &spaces[i]
			}
		}
		if space == nil {
			notify("Snooze", "No space to snooze here.")
			return 1, nil
		}
		panes, err := listPanes()
		if err != nil {
			return 1, err
		}
		env := spaceTarget(*space, panes).env()
		return openPicker(env, strconv.Itoa(pickerWidth), strconv.Itoa(pickerHeight(cfg)))

	case "agent":
		if readState().Panes[paneID] != nil {
			_, err := wake(cfg, []string{paneID}, nil, false) // the same key wakes it: one key, both ways
			return 0, err
		}
		panes, err := listPanes()
		if err != nil {
			return 1, err
		}
		var p *pane
		for i := range panes {
			if panes[i].PaneID == paneID {
				p = &panes[i]
			}
		}
		switch {
		case p == nil:
			notify("Snooze", "No pane to snooze here.")
			return 1, nil
		case p.Agent == "":
			notify("Snooze", "This pane has no agent, so there is nothing to hide.")
			return 1, nil
		}
		t := paneTarget(*p, ctx.WorkspaceLabel)
		return openPicker(t.env(), strconv.Itoa(pickerWidth), strconv.Itoa(pickerHeight(cfg)))
	}
	return 2, fmt.Errorf("unknown action %q", name)
}

// target is what the duration picker snoozes.
type target struct {
	Kind   string // "pane" or "workspace"
	ID     string
	Label  string
	Detail string
	Agent  *agentID // the agent shown, for a pane
	Status string   // its agent's status, for its mark
	// Snoozed: it is already, so the durations change when it wakes, and
	// it can be woken now.
	Snoozed bool
	// PR is its open pull request ("#482"), to snooze until it's merged.
	PR string
}

func paneTarget(p pane, workspace string) target {
	var parts []string
	for _, s := range []string{p.Agent, workspace} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	t := target{Kind: "pane", ID: p.PaneID, Label: paneLabel(p), Detail: strings.Join(parts, " · "),
		Agent: agentIdentity(p), Status: p.AgentStatus}
	if canWaitFor(whenMerged, p) {
		t.PR, _ = p.Tokens["pr"].(string)
	}
	return t
}

func spaceTarget(w workspaceInfo, panes []pane) target {
	agents := 0
	for _, p := range panes {
		if p.WorkspaceID == w.WorkspaceID && p.Agent != "" {
			agents++
		}
	}
	s := "s"
	if agents == 1 {
		s = ""
	}
	label := w.Label
	if label == "" {
		label = w.WorkspaceID
	}
	return target{Kind: "workspace", ID: w.WorkspaceID, Label: label,
		Detail: fmt.Sprintf("space · %d agent%s will be hidden", agents, s)}
}

func (t target) env() map[string]string {
	env := map[string]string{envKind: t.Kind, envTarget: t.ID, envLabel: t.Label, envDetail: t.Detail}
	if t.Agent != nil {
		data, _ := json.Marshal(t.Agent)
		env[envAgent] = string(data)
	}
	if t.Status != "" {
		env[envStatus] = t.Status
	}
	if t.PR != "" {
		env[envPR] = t.PR
	}
	return env
}

func targetFromEnv() target {
	t := target{Kind: os.Getenv(envKind), ID: os.Getenv(envTarget), Label: os.Getenv(envLabel),
		Detail: os.Getenv(envDetail), Status: os.Getenv(envStatus), PR: os.Getenv(envPR)}
	if t.Kind == "" {
		t.Kind = "pane"
	}
	if t.Label == "" {
		t.Label = t.ID
	}
	var a agentID
	if raw := os.Getenv(envAgent); raw != "" && json.Unmarshal([]byte(raw), &a) == nil && a.Agent != "" {
		t.Agent = &a
	}
	t.Label, t.Detail = clean(t.Label, false), clean(t.Detail, false)
	return t
}

// openPicker opens the popup. herdr shows one popup at a time, open as long
// as its command runs, on whichever client counts as active: the menu this
// action was picked from may still be closing (so ui_busy is retried a
// little), and with several clients attached (another terminal, a mosh
// session) the picker can be left open on one you're not looking at. Then
// it's ended and opened here; someone else's popup is theirs to close.
func openPicker(env map[string]string, width, height string) (int, error) {
	params := map[string]any{
		"plugin_id": pluginID(), "entrypoint": "picker", "placement": "popup", "focus": true,
		"width": sizeParam(width), "height": sizeParam(height), "env": env,
	}
	replacedOwn := false
	for attempt := 0; attempt < 5; attempt++ {
		err := herdrCall("plugin.pane.open", params, nil)
		if err == nil {
			return 0, nil
		}
		busy := isHerdrCode(err, "ui_busy")
		if busy && attempt >= 3 && !replacedOwn {
			if replacedOwn = endOwnPopup(); replacedOwn {
				continue
			}
			notify("Snooze", "Another popup is open: close it (esc) and try again.")
			logf("open failed: %v", err)
			return 1, nil
		}
		if !busy || attempt == 4 {
			notify("Snooze", "Couldn't open the picker: "+err.Error())
			logf("open failed: %v", err)
			return 1, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return 1, nil
}

// sizeParam is a popup size as herdr takes it: cells as a number, or a
// percentage as a string.
func sizeParam(s string) any {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return s
}

// popupFile holds the running picker's pid, so an open that finds it left
// open elsewhere can end it.
func popupFile() string { return filepath.Join(pluginStateDir(), "popup.pid") }

// notePopup records this process as the picker, and returns a func that
// forgets it again, unless a newer picker has replaced it.
func notePopup() func() {
	pid := strconv.Itoa(os.Getpid())
	if os.MkdirAll(pluginStateDir(), 0o700) != nil || os.WriteFile(popupFile(), []byte(pid), 0o600) != nil {
		return func() {}
	}
	return func() {
		if data, err := os.ReadFile(popupFile()); err == nil && strings.TrimSpace(string(data)) == pid {
			os.Remove(popupFile())
		}
	}
}

// endOwnPopup ends this plugin's picker if one is running, and waits for it
// to go: whether it was ours to end.
func endOwnPopup() bool {
	data, err := os.ReadFile(popupFile())
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 || !isOwnPopup(pid) {
		return false
	}
	if syscall.Kill(pid, syscall.SIGTERM) != nil {
		return false
	}
	for range 20 {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return syscall.Kill(pid, 0) != nil
}

// isOwnPopup checks the process is this program, not a stranger that was
// given the pid after ours exited.
func isOwnPopup(pid int) bool {
	self, err := os.Executable()
	if err != nil {
		return false
	}
	exe, err := processExe(pid)
	if err != nil {
		return false
	}
	a, errA := os.Stat(exe)
	b, errB := os.Stat(self)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// toggle steps the Agents panel through herdr's two orders, then the
// snoozed list, and round again. The orders are only ours to step through
// while nobody else orders the panel (see flipOrder); otherwise it is plain
// active <-> snoozed.
func toggle() error {
	cfg := loadConfig()
	return withState(func(st *state) error {
		v := &st.View
		ownOrder := v.Inherited == nil && cfg.Sort == nil
		ring := []string{"active", showingSnoozed}
		here := "active"
		if ownOrder {
			ring = []string{"priority", "spaces", showingSnoozed}
			if here = modeName(v.Order); here == "" {
				here = panelMode()
			}
		}
		if v.Showing == showingSnoozed {
			here = showingSnoozed
		}
		i := 0
		for j, r := range ring {
			if r == here {
				i = j
			}
		}
		there := ring[(i+1)%len(ring)]
		if len(st.Panes) == 0 {
			// Nothing to show or to order: herdr's own label does that.
			notify("Snooze", "Nothing is snoozed.")
			return nil
		}
		if there == showingSnoozed {
			v.Showing = showingSnoozed
			v.FromPane = invocationContext().FocusedPane
			if v.FromPane == "" {
				if panes, err := listPanes(); err == nil {
					v.FromPane = focusedPane(panes, false)
				}
			}
		} else {
			v.Showing, v.FromPane = "", ""
			if modeName(there) != "" {
				v.Order = there
			}
		}
		if _, err := syncState(st, cfg, syncOpts{}); err != nil {
			return err
		}
		if st.View.Applied == "" {
			// The last deadline had passed and this very sync cleared it. A
			// key press that does nothing needs a reason.
			notify("Snooze", "Nothing is snoozed.")
		}
		shown := there
		if st.View.Showing == showingSnoozed {
			shown = showingSnoozed
		}
		logf("toggle: showing %s", shown)
		return nil
	})
}

// flipOrder switches herdr's own panel order in its place while snooze
// holds the panel: herdr won't let its own label be clicked then. It lasts
// until the last snooze ends; then herdr's own setting is back.
func flipOrder() error {
	cfg := loadConfig()
	return withState(func(st *state) error {
		v := &st.View
		switch {
		case len(st.Panes) == 0:
			notify("Snooze", "Nothing is snoozed: click the panel's label to switch its order.")
			return nil
		case v.Inherited != nil:
			owner := v.Inherited.Source
			if owner == "" {
				owner = "another plugin"
			}
			notify("Snooze", fmt.Sprintf("The panel's order belongs to %s.", owner))
			return nil
		case cfg.Sort != nil:
			notify("Snooze", `config.json pins the panel's order ("sort").`)
			return nil
		}
		mode := panelMode()
		current := modeName(v.Order)
		if current == "" {
			current = mode
		}
		v.Order = "priority"
		if current == "priority" {
			v.Order = "spaces"
		}
		if _, err := syncState(st, cfg, syncOpts{}); err != nil {
			return err
		}
		if st.View.Applied == "" {
			notify("Snooze", "Nothing is snoozed: click the panel's label to switch its order.")
		}
		flipped := st.View.Order
		if flipped == "" {
			flipped = mode
		}
		logf("order: %s (herdr's own: %s)", flipped, mode)
		return nil
	})
}
