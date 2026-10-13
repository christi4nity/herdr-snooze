package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// agentID is which kind of agent a snooze was for.
type agentID struct {
	Agent string `json:"agent"`
}

func agentIdentity(p pane) *agentID {
	if p.Agent == "" {
		return nil
	}
	return &agentID{Agent: p.Agent}
}

// paneEntry is one snoozed pane. The field names are 0.2's (Python) file
// format, kept so snoozes survive the upgrade.
type paneEntry struct {
	Until int64 `json:"until"`
	// Via is "pane" (its own snooze) or "workspace" (its space's).
	Via         string   `json:"via,omitempty"`
	WorkspaceID string   `json:"workspace_id,omitempty"`
	TerminalID  string   `json:"terminal_id,omitempty"`
	AgentID     *agentID `json:"agent_id,omitempty"`
	// Since is when it was snoozed, ms.
	Since int64 `json:"since,omitempty"`
	// TokenUntil is when the token herdr holds dies, ms (its TTL is capped).
	TokenUntil int64 `json:"token_until,omitempty"`
	// Dirty: the token must be (re)sent whatever pane.list says.
	Dirty bool `json:"dirty,omitempty"`
	// AgentGone: the agent it was for has left; another arriving isn't it.
	AgentGone bool `json:"agent_gone,omitempty"`
	// When is a condition it's snoozed until (whenDone, whenMerged); Until
	// is then its cap.
	When string `json:"when,omitempty"`
	// Status is the agent's status when last seen, to tell a change by.
	Status string `json:"status,omitempty"`
}

// spaceEntry is one snoozed workspace.
type spaceEntry struct {
	Until      int64 `json:"until"`
	TokenUntil int64 `json:"token_until,omitempty"`
	Dirty      bool  `json:"dirty,omitempty"`
	// Except are panes woken early, or given their own deadline: the space
	// doesn't re-adopt them. ExceptTerminals lets that follow a moved pane.
	Except          []string          `json:"except,omitempty"`
	ExceptTerminals map[string]string `json:"except_terminals,omitempty"`
}

func (s *spaceEntry) excepts(pid string) bool {
	for _, e := range s.Except {
		if e == pid {
			return true
		}
	}
	return false
}

func (s *spaceEntry) addExcept(pid string) {
	if !s.excepts(pid) {
		s.Except = append(s.Except, pid)
	}
}

// inheritedView is the owner of the view snooze borrowed.
type inheritedView struct {
	Source string `json:"source"`
	Label  string `json:"label"`
}

// viewRecord is what snooze knows of herdr's agent view.
type viewRecord struct {
	// Applied is the fingerprint of the view snooze set, or "recover": it
	// may hold the view but doesn't know what it set.
	Applied   string         `json:"applied,omitempty"`
	Inherited *inheritedView `json:"inherited,omitempty"`
	// Showing is "snoozed" while the panel lists only what is snoozed.
	Showing string `json:"showing,omitempty"`
	// FromPane is where the snoozed list was opened from.
	FromPane string `json:"from_pane,omitempty"`
	// Order is the `order` command's flip of herdr's own panel order.
	Order string `json:"order,omitempty"`
}

func (v viewRecord) empty() bool { return v == viewRecord{} }

// recoverView means "we may hold the view, but don't know what we set": the
// next sync then asks herdr and clears it if it's ours.
func recoverView() viewRecord { return viewRecord{Applied: "recover"} }

type state struct {
	V          int                    `json:"v"`
	Panes      map[string]*paneEntry  `json:"panes"`
	Workspaces map[string]*spaceEntry `json:"workspaces"`
	View       viewRecord             `json:"view"`
	// Timer is when the sleeper will tick, ms; TimerPID is its process.
	Timer    *int64 `json:"timer"`
	TimerPID *int   `json:"timer_pid"`

	// Woke is the agents that woke by itself lately, by pane: the "Just
	// woke" list, until you go to one, snooze it again, or justWokeMS.
	Woke map[string]*wokeEntry `json:"woke"`

	// Woken and SpacesWoke are what woke by itself in this run, announced
	// once the state is saved. Never written: a run that saves nothing
	// announces nothing, and the next one finds the same wakes again.
	Woken      []woken  `json:"-"`
	SpacesWoke []string `json:"-"`
}

// wokeEntry is an agent on the "Just woke" list.
type wokeEntry struct {
	// At is when it woke, ms.
	At int64 `json:"at"`
	// Reason is what woke it ("it's done"); "" for time up.
	Reason  string   `json:"reason,omitempty"`
	AgentID *agentID `json:"agent_id,omitempty"`
	// Away: it has been seen not focused since it woke. Only then does
	// focus on it mean you went to it: the pane you snoozed is often the
	// one you stay on, and it would leave the list the moment it woke.
	Away bool `json:"away,omitempty"`
}

func emptyState() *state {
	return &state{V: 1, Panes: map[string]*paneEntry{}, Workspaces: map[string]*spaceEntry{},
		Woke: map[string]*wokeEntry{}}
}

func (s *state) idle() bool {
	return len(s.Panes) == 0 && len(s.Workspaces) == 0 && len(s.Woke) == 0 && s.View.empty()
}

func stateFile() string { return filepath.Join(stateDir(), "snoozed.json") }

// isInt is a JSON number with no fraction: Python's isinstance(v, int).
func isInt(raw json.RawMessage) bool {
	_, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	return err == nil
}

// strictDecode decodes raw into out, refusing a field of the wrong type.
func strictDecode(raw json.RawMessage, out any) bool {
	return json.Unmarshal(raw, out) == nil
}

// readState is the state file, or an empty state if there is none. A file
// that is there but can't be read in full (corrupt, a hand edit, a crash
// mid-write) is *not* taken as "nothing snoozed": tokens and our view may
// still be live in herdr, and dropping them unexamined could leave agents
// hidden with no record left to un-hide them. Such a state carries
// recoverView, so the next sync fails open: it clears tokens it has no record
// for, and our view.
func readState() *state {
	st := emptyState()
	data, err := os.ReadFile(stateFile())
	if errors.Is(err, os.ErrNotExist) {
		return st
	}
	var top map[string]json.RawMessage
	if err != nil || json.Unmarshal(data, &top) != nil || top == nil {
		// Unreadable, not JSON, or not an object: damaged.
		st.View = recoverView()
		return st
	}
	// Snooze writes every key, every time, and deletes the file when idle: a
	// file that exists without them (or with the wrong types) is damaged,
	// never a quiet "nothing snoozed". One malformed entry must not make
	// every hook crash, either: it's dropped, and the rest kept.
	damaged := false
	section := func(key string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		raw, ok := top[key]
		if !ok || json.Unmarshal(raw, &m) != nil || m == nil {
			damaged = true
			return nil
		}
		return m
	}
	for name, raw := range section("panes") {
		var until struct {
			Until json.RawMessage `json:"until"`
		}
		var e paneEntry
		if json.Unmarshal(raw, &until) != nil || !isInt(until.Until) || !strictDecode(raw, &e) {
			damaged = true
			continue
		}
		st.Panes[name] = &e
	}
	for name, raw := range section("workspaces") {
		var until struct {
			Until json.RawMessage `json:"until"`
		}
		var e spaceEntry
		if json.Unmarshal(raw, &until) != nil || !isInt(until.Until) || !strictDecode(raw, &e) {
			damaged = true
			continue
		}
		st.Workspaces[name] = &e
	}
	// Optional (0.4 had none), and only a list to show: an entry that
	// can't be read is dropped, not taken for damage.
	var woke map[string]json.RawMessage
	if json.Unmarshal(top["woke"], &woke) == nil {
		for name, raw := range woke {
			var e wokeEntry
			if strictDecode(raw, &e) && e.At > 0 {
				st.Woke[name] = &e
			}
		}
	}
	if view := section("view"); view != nil {
		if v, ok := decodeView(top["view"]); ok {
			st.View = v
		} else {
			damaged = true // its fields would mislead planning: recover instead
		}
	}
	if raw, ok := top["timer"]; ok && isInt(raw) {
		var t int64
		_ = json.Unmarshal(raw, &t)
		st.Timer = &t
	}
	if raw, ok := top["timer_pid"]; ok && isInt(raw) {
		var p int
		_ = json.Unmarshal(raw, &p)
		st.TimerPID = &p
	}
	if damaged && st.View.empty() {
		st.View = recoverView()
	}
	return st
}

// decodeView reads a saved view record, if every field has the type
// planning reads: strings, or for inherited an object of strings (or null).
func decodeView(raw json.RawMessage) (viewRecord, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return viewRecord{}, false
	}
	var v viewRecord
	str := func(key string, nullable bool, out *string) bool {
		r, ok := fields[key]
		if !ok {
			return true
		}
		var s *string
		if json.Unmarshal(r, &s) != nil || (s == nil && !nullable) {
			return false
		}
		if s != nil {
			*out = *s
		}
		return true
	}
	if !str("applied", false, &v.Applied) || !str("showing", false, &v.Showing) ||
		!str("from_pane", true, &v.FromPane) || !str("order", false, &v.Order) {
		return viewRecord{}, false
	}
	if r, ok := fields["inherited"]; ok && string(bytes.TrimSpace(r)) != "null" {
		var in map[string]*string
		if json.Unmarshal(r, &in) != nil {
			return viewRecord{}, false
		}
		v.Inherited = &inheritedView{}
		if s := in["source"]; s != nil {
			v.Inherited.Source = *s
		}
		if s := in["label"]; s != nil {
			v.Inherited.Label = *s
		}
	}
	return v, true
}

func (s *state) encode() []byte {
	data, _ := json.MarshalIndent(s, "", " ")
	return data
}

// withState runs f on the state under the state lock: hooks can overlap (two
// events in the same instant), so every read-modify-write of the state file
// happens under one flock. The state is saved after f, also when f fails: a
// herdr call can die after tokens were already sent, and a token with no
// record is an agent hidden for up to a day that no list shows. What we
// meant to do is the better record; the next tick reconciles whatever did
// not land.
func withState(f func(*state) error) error {
	root := stateDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(root, "lock"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	identPath := filepath.Join(root, "session.json")
	if _, err := os.Stat(identPath); err != nil {
		// Which session this directory belongs to, so pruneStaleSessions can
		// tell when that session no longer exists. Written under the lock
		// (the pruner takes it too), under a name only we use.
		tmp := identPath + "." + pidText(os.Getpid()) + ".tmp"
		if os.WriteFile(tmp, []byte(sessionIdentity("").pyJSON()), 0o600) == nil {
			os.Rename(tmp, identPath)
		}
	}
	st := readState()
	before := st.encode()
	ferr := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				saveState(st, before)
				panic(r)
			}
		}()
		return f(st)
	}()
	if err := saveState(st, before); err != nil {
		if ferr == nil {
			return err
		}
		return ferr
	}
	announceWakes(st.Woken, st.SpacesWoke)
	return ferr
}

func saveState(st *state, before []byte) error {
	path := stateFile()
	if st.idle() {
		// No file is the idle signal every hook checks first.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	after := st.encode()
	if bytes.Equal(after, before) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, after, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
