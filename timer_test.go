package main

import (
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// armer arms with stand-in sleepers: real processes that look like ours,
// so alive and stopped mean what they do for real ones.
type armer struct {
	t        *testing.T
	sleepers map[int]*sleeper
}

func newArmer(t *testing.T) *armer { return &armer{t: t, sleepers: map[int]*sleeper{}} }

func (a *armer) arm(st *state, now int64) []int64 {
	var delays []int64
	armTimer(st, now, func(delay int64) (int, error) {
		delays = append(delays, delay)
		s := startSleeper(a.t)
		a.sleepers[s.pid] = s
		return s.pid, nil
	})
	return delays
}

// armSelf arms with a stand-in that is always alive and never ours to stop:
// this test process.
func armSelf(st *state, now int64) []int64 {
	var delays []int64
	armTimer(st, now, func(delay int64) (int, error) {
		delays = append(delays, delay)
		return os.Getpid(), nil
	})
	return delays
}

func expectDelays(t *testing.T, got []int64, want ...int64) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delays %v, want %v", got, want)
	}
}

const safetyS = safetyMS / 1000

func TestArmsOnceForTheSoonestDeadline(t *testing.T) {
	a := newArmer(t)
	st := stateOf(map[string]*paneEntry{"a": {Until: now0 + 90_000}, "b": {Until: now0 + hourMS}}, nil)
	expectDelays(t, a.arm(st, now0), 91) // just after, never before
	if *st.Timer != now0+90_000 {
		t.Fatal(*st.Timer)
	}
	expectDelays(t, a.arm(st, now0)) // already armed for it, and the sleeper is alive
}

func TestRearmsWhenAnEarlierDeadlineAppears(t *testing.T) {
	a := newArmer(t)
	st := stateOf(map[string]*paneEntry{"a": {Until: now0 + 2*60_000}}, nil)
	a.arm(st, now0)
	first := a.sleepers[*st.TimerPID]
	st.Panes["b"] = &paneEntry{Until: now0 + 60_000}
	expectDelays(t, a.arm(st, now0), 61)
	if !first.gone(5 * time.Second) {
		t.Fatal("the replaced sleeper was left running")
	}
}

func TestNeverSleepsLongerThanTheSafetyInterval(t *testing.T) {
	// herdr has no event for another plugin taking the view; a tick at least
	// every few minutes is how snooze notices.
	st := stateOf(map[string]*paneEntry{"a": {Until: now0 + hourMS}}, nil)
	expectDelays(t, armSelf(st, now0), safetyS+1)
}

func TestADeadSleeperIsReplaced(t *testing.T) {
	a := newArmer(t)
	st := stateOf(map[string]*paneEntry{"a": {Until: now0 + 60_000}}, nil)
	a.arm(st, now0)
	a.sleepers[*st.TimerPID].kill() // killed, or the machine slept through it
	expectDelays(t, a.arm(st, now0), 61)
}

func TestWorkspacesCount(t *testing.T) {
	st := stateOf(nil, map[string]*spaceEntry{"w": {Until: now0 + 30_000}})
	expectDelays(t, armSelf(st, now0), 31)
}

func TestLongSnoozeWakesUpInTimeToRefreshItsToken(t *testing.T) {
	// herdr caps a token at 24h. On a machine quiet enough that no hook fires
	// (a weekend), nothing else would re-report it.
	st := stateOf(map[string]*paneEntry{"a": {Until: now0 + 7*24*hourMS, Dirty: true}}, nil)
	rec(st, []pane{mkPane("a", nil)}, now0)
	armSelf(st, now0)
	now := now0
	var got []tokenOp
	for now < now0+12*hourMS+2000 {
		now = *st.Timer
		if got = rec(st, []pane{mkPane("a", liveToken("z"))}, now); len(got) > 0 {
			break
		}
		armSelf(st, now)
	}
	if !reflect.DeepEqual(kinds(got), []string{opReport}) || now > now0+12*hourMS+2000 {
		t.Fatalf("not refreshed in time: %v at +%dms", got, now-now0)
	}
}

func TestShortSnoozeIsNotWokenForARefreshItDoesNotNeed(t *testing.T) {
	st := stateOf(map[string]*paneEntry{"a": {Until: now0 + 60_000, Dirty: true}}, nil)
	rec(st, []pane{mkPane("a", nil)}, now0)
	expectDelays(t, armSelf(st, now0), 61)
}

func TestRealSleepersAreRecognisedAndOnlyOursAreStopped(t *testing.T) {
	if exec.Command("ps", "-o", "command=", "-p", "1").Run() != nil {
		t.Skip("ps isn't available here")
	}
	// The real launcher, so this tests the contract rather than a copy of
	// it. The imposter even borrows our $0 name, but runs something else.
	pid, err := realSpawnTimer(30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		syscall.Kill(-pid, syscall.SIGKILL)
		var ws syscall.WaitStatus
		syscall.Wait4(pid, &ws, 0, nil)
	})
	other := exec.Command("/bin/sh", "-c", "sleep 30 && true", timerName)
	other.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	otherDone := make(chan struct{})
	go func() { other.Wait(); close(otherDone) }()
	t.Cleanup(func() { other.Process.Kill(); <-otherDone })

	if !timerAlive(&pid) || !isOurTimer(pid) {
		t.Fatal("our sleeper not recognised")
	}
	if isOurTimer(other.Process.Pid) {
		t.Fatal("the imposter taken for ours")
	}
	otherPID := other.Process.Pid
	stopTimer(&otherPID) // a reused PID: must survive
	stopTimer(&pid)
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil || !ws.Signaled() {
		t.Fatalf("ours not stopped: %v %v", err, ws)
	}
	select {
	case <-otherDone:
		t.Fatal("stopped someone else's process")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNothingSnoozedDisarms(t *testing.T) {
	st := emptyState()
	at := int64(123)
	st.Timer = &at
	expectDelays(t, armSelf(st, now0))
	if st.Timer != nil {
		t.Fatal(*st.Timer)
	}
}

func TestSpawnFailureIsNotFatal(t *testing.T) {
	st := stateOf(map[string]*paneEntry{"a": {Until: now0 + 60_000}}, nil)
	armTimer(st, now0, func(int64) (int, error) { return 0, errors.New("no fork for you") })
	if st.Timer != nil {
		t.Fatal("recorded a sleeper that never started") // so the next sync tries again
	}
}

func TestTheSleeperTicksAsATimer(t *testing.T) {
	if !strings.HasSuffix(timerScript, "tick --timer") {
		t.Fatal(timerScript)
	}
}
