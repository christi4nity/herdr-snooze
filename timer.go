package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// safetyMS is the longest between ticks while anything is snoozed,
	// whatever the deadlines: herdr has no event for another plugin taking
	// the agent view, so this is how snooze notices (and how a dead sleeper
	// is noticed on a quiet machine).
	safetyMS    = 5 * 60 * 1000
	timerShell  = "/bin/sh"
	timerScript = `sleep "$1" && exec "$2" tick --timer`
	// timerName is the sleeper's $0, so it can be told apart from a process
	// that was given its PID later.
	timerName = "snooze-timer"
)

// spawnTimer starts a detached sleeper that ticks after delay seconds, and
// returns its PID. A variable so tests can stand one in.
var spawnTimer = func(delay int64) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(timerShell, "-c", timerScript, timerName, strconv.FormatInt(delay, 10), self)
	// Its own session: it outlives this process, and stopping it stops the
	// sleep with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

// timerAlive is whether our sleeper is still there. A signal-0 probe:
// cheap, but a PID can be reused, so stopping it goes through isOurTimer
// first.
func timerAlive(pid *int) bool {
	if pid == nil || *pid <= 0 {
		return false
	}
	err := syscall.Kill(*pid, 0)
	// EPERM: someone else's process has that PID now.
	return err == nil
}

// isOurTimer is whether the process is one of our sleepers: its whole
// command line, not a word in it, before we'd stop its process group.
func isOurTimer(pid int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	return strings.HasPrefix(string(out), timerShell+" -c "+timerScript+" "+timerName+" ")
}

func stopTimer(pid *int) {
	if timerAlive(pid) && isOurTimer(*pid) {
		_ = syscall.Kill(-*pid, syscall.SIGTERM) // its own group: the sleep goes with it
	}
}

// armTimer keeps one detached sleeper that runs a tick at the next moment
// something needs doing (a deadline, a token due for refresh, or safetyMS at
// the latest).
//
// herdr's TTL already un-hides the agent on time; what nothing does on time
// is *us* noticing: the label keeps its count, the view stays borrowed, and
// in the snoozed list the last agent expiring leaves an empty panel. And a
// snooze longer than the TTL cap must be re-reported before its token dies.
// Not a daemon: each sleeper ticks once and is gone. Its PID is kept, so a
// sleeper that died (killed, a machine sleep) is replaced by the next tick
// instead of being trusted until the snooze ends early.
func armTimer(st *state, now int64, spawn func(int64) (int, error)) {
	type due struct{ until, tokenUntil int64 }
	var entries []due
	for _, e := range st.Panes {
		entries = append(entries, due{e.Until, e.TokenUntil})
	}
	for _, e := range st.Workspaces {
		entries = append(entries, due{e.Until, e.TokenUntil})
	}
	if len(entries) == 0 {
		stopTimer(st.TimerPID)
		st.Timer, st.TimerPID = nil, nil
		return
	}
	soonest := int64(-1)
	for _, e := range entries {
		at := e.until
		if e.tokenUntil != 0 && e.tokenUntil < e.until {
			at = min(e.until, e.tokenUntil-refreshMarginMS+1000)
		}
		if soonest < 0 || at < soonest {
			soonest = at
		}
	}
	if st.Timer != nil && now < *st.Timer && *st.Timer <= soonest && timerAlive(st.TimerPID) {
		return // a live sleeper will tick in time, and re-arm then
	}
	wakeAt := min(soonest, now+safetyMS)
	pid, err := spawn(max(1, (wakeAt-now)/1000+1))
	if err != nil {
		logf("timer not armed: %v", err) // hooks still catch up, just later
		return
	}
	stopTimer(st.TimerPID) // the one it replaces, if it's still sleeping
	st.Timer, st.TimerPID = &wakeAt, &pid
}

// armRetry is for a failed --timer tick: a sleeper safetyMS out, recorded
// as the sleeper so the next tick counts on it rather than starting another.
// Only in place of this tick's own (the sleeper exec'd into it, keeping its
// PID) or a dead one: a live sleeper another tick armed meanwhile is sooner
// or as good, and stays.
func armRetry() error {
	return withState(func(st *state) error {
		if st.idle() {
			return nil
		}
		if cur := st.TimerPID; cur != nil && *cur != os.Getpid() && timerAlive(cur) && isOurTimer(*cur) {
			return nil
		}
		pid, err := spawnTimer(safetyMS / 1000)
		if err != nil {
			return err
		}
		at := nowMS() + safetyMS
		st.Timer, st.TimerPID = &at, &pid
		return nil
	})
}
