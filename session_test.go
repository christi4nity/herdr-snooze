package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// Pane IDs only mean something inside one herdr session; the plugin's state
// directory is shared by all of them.

type sessions struct {
	t     *testing.T
	dir   string
	state string
}

func newSessions(t *testing.T) *sessions {
	dir := t.TempDir()
	s := &sessions{t: t, dir: dir, state: filepath.Join(dir, "state")}
	t.Setenv("SNOOZE_STATE_DIR", s.state)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	return s
}

// session is a herdr session's directory, as herdr makes one, and its socket.
func (s *sessions) session(name string) string {
	home := filepath.Join(s.dir, "sessions", name)
	os.MkdirAll(home, 0o700)
	return filepath.Join(home, "herdr.sock")
}

func (s *sessions) in(sock string, f func()) {
	s.t.Setenv("HERDR_SOCKET_PATH", sock)
	f()
}

func (s *sessions) snoozeIn(sock string) {
	s.in(sock, func() {
		withState(func(st *state) error {
			st.Panes["w1:p1"] = &paneEntry{Until: nowMS() + hourMS, Via: viaPane}
			return nil
		})
	})
}

func (s *sessions) dirOf(sock string) (dir string) {
	s.in(sock, func() { dir = stateDir() })
	return dir
}

func TestEverySessionHasItsOwnState(t *testing.T) {
	s := newSessions(t)
	work, play := s.session("work"), s.session("play")
	s.snoozeIn(work)
	a := s.dirOf(work)
	s.in(work, func() {
		if readState().Panes["w1:p1"] == nil {
			t.Fatal("lost")
		}
	})
	s.in(play, func() {
		if stateDir() == a || len(readState().Panes) != 0 {
			t.Fatal("play sees work's w1:p1")
		}
	})
	if !strings.HasPrefix(a, filepath.Join(s.state, "sessions")) {
		t.Fatal(a)
	}
}

func TestARecreatedSessionNeverReadsTheOldOnesRecords(t *testing.T) {
	// herdr numbers a new session's panes from w1:p1 again.
	s := newSessions(t)
	work := s.session("work")
	s.snoozeIn(work)
	os.RemoveAll(filepath.Dir(work)) // herdr session delete work
	work = s.session("work")         // …and a new "work"
	s.in(work, func() {
		if len(readState().Panes) != 0 {
			t.Fatal(readState().Panes)
		}
	})
}

func TestStateOfADeletedSessionIsPruned(t *testing.T) {
	s := newSessions(t)
	work, play := s.session("work"), s.session("play")
	s.snoozeIn(work)
	s.snoozeIn(play)
	workState, playState := s.dirOf(work), s.dirOf(play)
	os.RemoveAll(filepath.Dir(work)) // work is deleted
	s.in(play, pruneStaleSessions)
	if exists(workState) {
		t.Fatal("a deleted session's file keeps every hook working")
	}
	if !exists(filepath.Join(playState, "snoozed.json")) {
		t.Fatal("pruned the living")
	}
}

func TestAStoppedOrRestartedSessionKeepsItsState(t *testing.T) {
	s := newSessions(t)
	work, play := s.session("work"), s.session("play")
	s.snoozeIn(work)
	s.in(play, pruneStaleSessions) // work's directory is still there: only its server is down
	s.in(work, func() {
		if readState().Panes["w1:p1"] == nil {
			t.Fatal("pruned")
		}
	})
}

func TestIdentityDoesntDependOnTheFileSystem(t *testing.T) {
	// The session ID is random and lives in the session's own directory.
	s := newSessions(t)
	work := s.session("work")
	first := sessionIdentity(work)
	if sessionIdentity(work) != first || first.ID == "" {
		t.Fatal("not stable while the directory lives", first)
	}
	os.Remove(filepath.Join(filepath.Dir(work), sessionIDFile)) // what deleting the session does to it
	if sessionIdentity(work).ID == first.ID {
		t.Fatal("the same ID after a delete")
	}
}

func TestASessionBeingSetUpIsNeverPruned(t *testing.T) {
	// Its directory exists before its identity is written.
	s := newSessions(t)
	work, play := s.session("work"), s.session("play")
	half := s.dirOf(work)
	os.MkdirAll(half, 0o700)
	s.in(play, pruneStaleSessions)
	if !exists(half) {
		t.Fatal("pruned")
	}
}

func TestABusyStaleSessionIsLeftForLater(t *testing.T) {
	s := newSessions(t)
	work, play := s.session("work"), s.session("play")
	s.snoozeIn(work)
	stale := s.dirOf(work)
	os.RemoveAll(filepath.Dir(work))
	f, _ := os.OpenFile(filepath.Join(stale, "lock"), os.O_APPEND|os.O_WRONLY, 0o600)
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX) // something is writing it
	s.in(play, pruneStaleSessions)
	if !exists(stale) {
		t.Fatal("pruned while busy")
	}
	f.Close()
	s.in(play, pruneStaleSessions)
	if exists(stale) {
		t.Fatal("never pruned")
	}
}

func TestTwoFirstRunsAgreeOnOneID(t *testing.T) {
	// Separate processes, as hooks are.
	sock := newSessions(t).session("race")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(self)
			cmd.Env = append(os.Environ(), "SNOOZE_TEST_SESSION_ID="+sock)
			out, _ := cmd.Output()
			ids[i] = string(out)
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatal(ids)
		}
	}
}

func TestHerdrsOwnSessionsKeepThe011IDFile(t *testing.T) {
	// Renaming it would re-key every 0.1.1 session and orphan its state.
	sock := newSessions(t).session("work")
	if got := sessionIDPath(sock); got != filepath.Join(filepath.Dir(sock), sessionIDFile) {
		t.Fatal(got)
	}
}

func TestCustomSocketsSharingADirectoryDontShareAnIdentity(t *testing.T) {
	s := newSessions(t)
	shared := filepath.Join(s.dir, "tmp")
	os.MkdirAll(shared, 0o700)
	a, b := filepath.Join(shared, "a.sock"), filepath.Join(shared, "b.sock")
	if sessionID(a) == sessionID(b) {
		t.Fatal("shared")
	}
	os.Remove(sessionIDPath(a)) // a's server is gone; b's identity must stand
	var key string
	s.in(b, func() { key = sessionKey(sessionIdentity("")) })
	if sessionKey(identity{Socket: realPath(b), ID: readSessionID(b)}) != key {
		t.Fatal("b's key changed")
	}
}

// 0.2 (Python) keyed each session's state directory by a hash of
// json.dumps({"socket": …, "id": …}, sort_keys=True). The keys must come out
// the same, or an upgrade loses every snooze.
func TestSessionKeysMatchPython(t *testing.T) {
	for _, c := range []struct {
		socket, id, want string
	}{
		{"/home/u/.config/herdr/herdr.sock", "0123abcd", "2564ee9ee8c2"},
		{"/tmp/hérdr/日本/x.sock", "", "63c5f14c1684"}, // no ID: null
		{"/tmp/z💤\"q\\b/herdr.sock", "abc", "1aed575aae35"},
		{"/tmp/a<b>&c/herdr.sock", "f00", "02b4889408a5"},
	} {
		if got := sessionKey(identity{Socket: c.socket, ID: c.id}); got != c.want {
			t.Errorf("%q: %s, want %s", c.socket, got, c.want)
		}
	}
}

func TestSessionJSONRoundTrips(t *testing.T) {
	for _, id := range []identity{{"/tmp/a.sock", "x"}, {"/tmp/hérdr.sock", ""}} {
		var back identity
		data, _ := id.MarshalJSON()
		if err := back.UnmarshalJSON(data); err != nil || back != id {
			t.Fatal(id, back, err)
		}
	}
}
