package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

const showingSnoozed = "snoozed" // viewRecord.Showing

// viewFilter hides what is snoozed.
var viewFilter = map[string]any{"op": "not", "filter": map[string]any{"op": "exists", "field": map[string]any{"token": mark}}}

// snoozedListFilter is the panel flipped to list what is snoozed. It also
// lets blocked agents through: it is the one panel state that hides your
// active agents, and one of them needing you is exactly what the panel
// exists to show.
var snoozedListFilter = map[string]any{"op": "any", "filters": []any{
	map[string]any{"op": "exists", "field": map[string]any{"token": mark}},
	map[string]any{"op": "eq", "field": "status", "value": "blocked"},
}}

func sortKey(field any, order string) map[string]any {
	return map[string]any{"field": field, "order": order}
}

func tokenField(name string) map[string]any { return map[string]any{"token": name} }

// knownView is what snooze knows of another plugin's view. herdr has a
// single agent view and its query is not readable: the only thing a caller
// can learn is the owner's source and label. So borrowing the view from a
// plugin that sorts the panel means re-stating its sort ourselves, and this
// table is how we know it. Unknown owners lose their sort while something is
// snoozed; config.json's "views" teaches snooze about one, or "sort" pins a
// sort outright.
type knownView struct {
	sorts map[string][]any
	// offFlag is a file in the owner's state directory that, holding
	// offValue, says its view is off: radar turns its view off with a
	// source-matched clear, which is a no-op while we hold the view, so its
	// persisted intent is the only trace.
	offPlugin, offFile, offValue string
}

var knownViews = map[string]knownView{
	"herdr-agent-inbox": {sorts: map[string][]any{
		"Inbox": {sortKey(tokenField("rank"), "asc"), sortKey("state_change_seq", "desc")},
	}},
	"plugin:hhdebb.herdr-radar": {
		sorts: map[string][]any{
			"active": {sortKey(tokenField("ws_key"), "desc"), sortKey(tokenField("tab_key"), "desc"), sortKey(tokenField("sort_key"), "desc")},
			"recent": {sortKey(tokenField("sort_key"), "desc")},
		},
		offPlugin: "hhdebb.herdr-radar", offFile: "agent-view.on", offValue: "off",
	},
}

// knownSort is the sort behind another plugin's view, if we know it. views
// is the user's config.json table, and wins over the built-in one.
func knownSort(owner *inheritedView, views map[string]map[string][]any) []any {
	if owner == nil {
		return nil
	}
	if s, ok := views[owner.Source][owner.Label]; ok {
		return s
	}
	if k, ok := knownViews[owner.Source]; ok {
		return k.sorts[owner.Label]
	}
	return nil
}

// panelModes are herdr's own panel orders, for when nobody else holds the
// view. Any view hides the panel's "grouped"/"priority" label (and its
// toggle) behind the view's own label, and a view with no sort gets the
// server's config.toml order, not the one toggled in the client. So we
// re-state both. The sorts are herdr 0.9.1's (src/app/agent_view.rs): spaces
// is plain workspace/tab/pane order, priority is attention then latest state
// change.
var panelModes = map[string]struct {
	label string
	sort  []any
}{
	"spaces":   {"grouped", []any{sortKey("workspace_order", "asc"), sortKey("tab_order", "asc"), sortKey("pane_order", "asc")}},
	"priority": {"priority", []any{sortKey("attention", "desc"), sortKey("state_change_seq", "desc")}},
}

func modeName(v string) string {
	if v == "workspaces" {
		v = "spaces"
	}
	if _, ok := panelModes[v]; ok {
		return v
	}
	return ""
}

func fnv1a64(s string) uint64 {
	v := uint64(0xcbf29ce484222325)
	for _, b := range []byte(s) {
		v = (v ^ uint64(b)) * 0x100000001b3
	}
	return v
}

// panelMode is herdr's current panel order: "spaces" or "priority". A
// toggle in the client wins; herdr saves it per client, in a file named by a
// hash of the client socket (the API socket with "-client" before ".sock").
// Otherwise [ui] agent_panel_sort in config.toml, otherwise herdr's default.
func panelMode() string {
	sock := socketPath()
	client := sock + "-client"
	if root, ok := strings.CutSuffix(sock, ".sock"); ok && filepath.Ext(sock) == ".sock" {
		client = root + "-client.sock"
	}
	prefs := filepath.Join(xdgDir("XDG_STATE_HOME", ".local/state"), "herdr", "client-shell", fmt.Sprintf("local-%016x.json", fnv1a64(client)))
	if data, err := os.ReadFile(prefs); err == nil {
		var p struct {
			Sort string `json:"agent_panel_sort"`
		}
		if json.Unmarshal(data, &p) == nil {
			if m := modeName(p.Sort); m != "" {
				return m
			}
		}
	}
	var cfg struct {
		UI struct {
			Sort *string `toml:"agent_panel_sort"`
		} `toml:"ui"`
	}
	if data, err := os.ReadFile(herdrConfigPath()); err == nil {
		if _, err := toml.Decode(string(data), &cfg); err == nil && cfg.UI.Sort != nil {
			if m := modeName(*cfg.UI.Sort); m != "" {
				return m
			}
		}
	}
	return "spaces"
}

// ownerTurnedOff is whether the owner's own record says its view is off.
func ownerTurnedOff(owner *inheritedView) bool {
	if owner == nil {
		return false
	}
	k, ok := knownViews[owner.Source]
	if !ok || k.offPlugin == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(herdrStateRoot(), k.offPlugin, k.offFile))
	return err == nil && strings.TrimSpace(string(data)) == k.offValue
}

// settleShowing: the snoozed list is somewhere you visit, not a mode to get
// stuck in: a panel that keeps listing only snoozed agents hides the one that
// just got blocked. So it ends by itself once focus lands on a pane that is
// not snoozed — other than the pane you opened it from, which is where focus
// still is while you read the list. Returns true when it switched back.
func settleShowing(st *state, focused string) bool {
	v := &st.View
	if v.Showing != showingSnoozed || focused == "" {
		return false
	}
	if _, snoozed := st.Panes[focused]; snoozed {
		v.FromPane = "" // went to look at one; any other pane now means "done here"
		return false
	}
	if focused == v.FromPane {
		return false
	}
	v.Showing, v.FromPane = "", ""
	return true
}

// viewAction is a call to make on herdr's agent view.
type viewAction struct {
	Method string // "agent.view.set" or "agent.view.clear"
	Params map[string]any
}

// viewPlan is what planView needs to know besides the state.
type viewPlan struct {
	owner    viewOwner
	count    int // how many *agents* are snoozed
	blocked  int // how many active ones are blocked
	mode     string
	ownerOff bool
	cfg      config
}

// planView decides what to do with herdr's single agent view. owner is the
// probe result; mode is herdr's own panel order (panelMode), kept when there
// is no other owner to borrow from. It updates st.View and returns the call
// to make, or nil.
func planView(st *state, p viewPlan) *viewAction {
	view := st.View
	mine := p.owner.Active && p.owner.Source == source()
	inherited := view.Inherited
	if p.owner.Active && !mine {
		inherited = &inheritedView{Source: p.owner.Source, Label: p.owner.Label}
		// If we had the snoozed list up and it got replaced, someone just
		// worked their own panel control. Re-taking the view is how hiding
		// keeps working; putting the snoozed list back on top would undo
		// what they asked for — and the safe side to land on is normal. (Not
		// when the list was only just asked for and isn't up yet: that is a
		// toggle arriving while another plugin happens to hold the view.)
		if hadListUp(view.Applied) {
			view.Showing, view.FromPane = "", ""
		}
	} else if !p.owner.Active {
		inherited = nil
	}
	if p.ownerOff {
		inherited = nil
	}

	// count, not st.Panes: a record with no agent behind it (a shell, an
	// agent that exited) hides nothing, and a filter held for it would make
	// the snoozed list an empty panel.
	if p.count > 0 {
		// order is the `order` command's flip of herdr's own order: herdr
		// won't let its label be clicked while we hold the view. It lasts
		// only while we do (herdr keeps its own mode in the client), so the
		// label marks it with "*".
		var order, nativeLabel string
		var nativeSort []any
		if inherited == nil {
			order = modeName(view.Order)
			m := order
			if m == "" {
				m = p.mode
			}
			if pm, ok := panelModes[m]; ok {
				nativeLabel, nativeSort = pm.label, pm.sort
			}
			if order != "" && order != p.mode && nativeLabel != "" {
				nativeLabel += "*"
			}
		}
		base := nativeLabel
		if inherited != nil {
			base = inherited.Label
		}
		// Showing is the toggle: the same panel, flipped to list what is
		// snoozed. The two labels must not be confusable — one means "N are
		// hidden from this list", the other "this list IS the hidden ones".
		showing := view.Showing == showingSnoozed
		var label string
		switch {
		case showing:
			label = badged(p.cfg.Badge, fmt.Sprintf("snoozed only · %d", p.count))
			if p.blocked > 0 {
				label += fmt.Sprintf(" + %d blocked", p.blocked)
			}
		case base != "":
			label = base + " " + badged(p.cfg.Badge, fmt.Sprint(p.count))
		default:
			label = badged(p.cfg.Badge, fmt.Sprint(p.count))
		}
		filter := viewFilter
		if showing {
			filter = snoozedListFilter
		}
		sort := p.cfg.Sort
		if sort == nil {
			sort = knownSort(inherited, p.cfg.Views)
		}
		if sort == nil {
			sort = nativeSort
		}
		if sort == nil {
			sort = []any{}
		}
		want := map[string]any{"source": source(), "label": strings.TrimSpace(label), "filter": filter, "sort": sort}
		fp, _ := json.Marshal(want)
		next := viewRecord{Inherited: inherited, Applied: string(fp)}
		if order != "" && order != p.mode {
			next.Order = order
		}
		if showing {
			next.Showing, next.FromPane = showingSnoozed, view.FromPane
		}
		st.View = next
		if mine && view.Applied == string(fp) {
			return nil
		}
		return &viewAction{"agent.view.set", want}
	}

	st.View = viewRecord{}
	if !mine {
		return nil
	}
	if sort := knownSort(inherited, p.cfg.Views); inherited != nil && sort != nil {
		// Hand the borrowed view back exactly as we found it, under its
		// owner's source, so that owner's own toggles keep working.
		return &viewAction{"agent.view.set", map[string]any{"source": inherited.Source, "label": inherited.Label, "sort": sort}}
	}
	return &viewAction{"agent.view.clear", map[string]any{"source": source()}}
}

// hadListUp is whether the view snooze set (by its fingerprint) was the
// snoozed list.
func hadListUp(applied string) bool {
	var fp struct {
		Filter any `json:"filter"`
	}
	if json.Unmarshal([]byte(applied), &fp) != nil {
		return false
	}
	var want any
	data, _ := json.Marshal(snoozedListFilter)
	_ = json.Unmarshal(data, &want)
	return reflect.DeepEqual(fp.Filter, want)
}
