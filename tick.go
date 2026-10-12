package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

// tick is what every hook runs. Nothing snoozed anywhere (no state file)
// means nothing to do, and it returns before anything else: hooks fire on
// every focus change and every agent status flip.
//
// Hooks fire in bursts (herdr runs them concurrently): a tick that finds
// another one running leaves a note and exits, and the running one goes
// again — a burst is a couple of passes, not a queue of processes each
// waiting on the lock and on herdr. Every pass reads everything from herdr,
// so plain ticks carry nothing a later pass would miss. Two run regardless:
// the startup --force (it resets what a restart left behind) and a
// --detected one, whose event (an agent leaving or arriving) no later pass
// could see.
//
// No note is ever stranded: the runner looks again after unlocking, and a
// tick that left a note tries the lock once more, so either the note's
// writer runs or the lock's holder will see the note after unlocking.
//
// A --timer tick is the detached sleeper's; if it fails (herdr busy or
// restarting), nothing else may come along to arm the next one, so it arms
// a retry itself while herdr's socket is there.
func tick(args []string) error {
	if !anythingSnoozed() {
		return nil
	}
	has := func(flag string) bool { return contains(args, flag) }
	err := tickCoalesced(has)
	if err != nil && has("--timer") {
		if _, statErr := os.Stat(socketPath()); statErr == nil {
			if rerr := armRetry(); rerr != nil {
				logf("retry not armed: %v", rerr)
			}
		}
	}
	return err
}

func tryLock(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

func tickCoalesced(has func(string) bool) error {
	if has("--detected") {
		received := nowMS() // before any lock: agentDetected dates the event by it
		var ev any
		_ = json.Unmarshal([]byte(os.Getenv("HERDR_PLUGIN_EVENT_JSON")), &ev)
		detected, ok := findDetected(ev)
		return tickOnce(has("--force"), &detected, ok, received)
	}
	root := stateDir()
	if st, err := os.Stat(root); has("--force") || err != nil || !st.IsDir() {
		return tickOnce(has("--force"), nil, false, 0)
	}
	pending := filepath.Join(root, "tick.pending")
	lock, err := os.OpenFile(filepath.Join(root, "tick.lock"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	exists := func() bool { _, err := os.Stat(pending); return err == nil }
	for {
		if !tryLock(lock) {
			if f, err := os.OpenFile(pending, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
				f.Close()
			}
			if !tryLock(lock) {
				return nil // its holder hasn't unlocked yet: it will see the note
			}
		}
		for {
			os.Remove(pending)
			if err := tickOnce(false, nil, false, 0); err != nil {
				syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
				return err
			}
			if !exists() {
				break
			}
		}
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		if !exists() {
			return nil
		}
	}
}

func tickOnce(force bool, detected *detectedEvent, hasDetected bool, received int64) error {
	pruneStaleSessions()
	cfg := loadConfig()
	if _, err := os.Stat(filepath.Join(pluginStateDir(), "snoozed.json")); err == nil {
		recoverLegacy(cfg)
	}
	if readState().idle() {
		// Nothing to do. If a file is there anyway (every entry in it was
		// malformed and dropped), remove it: while it exists, every hook in
		// every session does this much work.
		if _, err := os.Stat(stateFile()); err == nil {
			return withState(func(*state) error { return nil })
		}
		return nil
	}
	return withState(func(st *state) error {
		if hasDetected && agentDetected(st, *detected, received) {
			what := "changed"
			if detected.Released {
				what = "released"
			}
			logf("agent %s in %s - its snooze was for the one before", what, detected.PaneID)
		}
		_, err := syncState(st, cfg, syncOpts{force: force})
		return err
	})
}
