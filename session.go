package main

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

// sessionIDFile is a random ID snooze keeps next to the herdr session's
// socket, naming this incarnation of the session.
const sessionIDFile = ".herdr-snooze-session"

// sessionIDPath is where a socket's session ID lives. herdr's own sessions
// each have a directory with one herdr.sock in it, and keep the 0.1.1 name.
// A socket named anything else (a custom HERDR_SOCKET_PATH, perhaps one of
// several in /tmp) gets a file of its own, so sockets sharing a directory
// don't share an identity.
func sessionIDPath(sock string) string {
	home, name := filepath.Split(sock)
	if name == "herdr.sock" {
		return filepath.Join(home, sessionIDFile)
	}
	return filepath.Join(home, sessionIDFile+"-"+name)
}

func readSessionID(sock string) string {
	data, err := os.ReadFile(sessionIDPath(sock))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// sessionID is the ID of the herdr session listening on sock, created on
// first use. A server restart keeps its directory and so the ID (snoozes are
// meant to survive it). Deleting a named session removes its directory, and
// with it the ID: a new session of the same name — whose panes herdr numbers
// from w1:p1 again — gets a new one, so it never reads the old one's records.
// "" if the directory can't be written (then only the socket identifies the
// session).
func sessionID(sock string) string {
	if id := readSessionID(sock); id != "" {
		return id
	}
	path := sessionIDPath(sock)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	if err := os.WriteFile(tmp, []byte(hex.EncodeToString(raw[:])+"\n"), 0o644); err != nil {
		return ""
	}
	defer os.Remove(tmp)
	// link, not rename: it fails if another process got there first, and
	// nobody ever reads a half-written file.
	if err := os.Link(tmp, path); err != nil && !errors.Is(err, os.ErrExist) {
		return ""
	}
	return readSessionID(sock)
}

// realPath is Python's os.path.realpath: symlinks resolved as far as the
// path exists, the rest kept as it is.
func realPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	dir, base := filepath.Split(abs)
	dir = filepath.Clean(dir)
	if dir == abs {
		return abs
	}
	return filepath.Join(realPath(dir), base)
}

// identity is what makes a herdr session this one: its socket and its
// session ID ("" when it has none).
type identity struct {
	Socket string
	ID     string
}

func sessionIdentity(sock string) identity {
	if sock == "" {
		sock = socketPath()
	}
	sock = realPath(sock)
	return identity{Socket: sock, ID: sessionID(sock)}
}

// pyJSON is the identity as Python's json.dumps(…, sort_keys=True) wrote it:
// 0.2's session keys are hashes of exactly these bytes, so state written
// before the rewrite in Go is found again.
func (id identity) pyJSON() string {
	idText := "null"
	if id.ID != "" {
		idText = pyString(id.ID)
	}
	return `{"id": ` + idText + `, "socket": ` + pyString(id.Socket) + `}`
}

func (id identity) MarshalJSON() ([]byte, error) { return []byte(id.pyJSON()), nil }

func (id *identity) UnmarshalJSON(data []byte) error {
	var raw struct {
		Socket *string `json:"socket"`
		ID     *string `json:"id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Socket == nil {
		return errors.New("no socket")
	}
	id.Socket = *raw.Socket
	id.ID = ""
	if raw.ID != nil {
		id.ID = *raw.ID
	}
	return nil
}

// pyString is a string as Python's json.dumps writes it, with ensure_ascii:
// everything outside ASCII as \u escapes (UTF-16, as Python does).
func pyString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20 || (r >= 0x7f && r <= 0xffff):
			fmt.Fprintf(&b, `\u%04x`, r)
		case r > 0xffff:
			r1, r2 := utf16.EncodeRune(r)
			fmt.Fprintf(&b, `\u%04x\u%04x`, r1, r2)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// sessionKey is a short key for one herdr session. Pane and workspace IDs
// only mean something inside one herdr session, but the plugin's state
// directory is shared by all of them: without a key per session, a second
// session would read the first one's snoozes, hide its own unrelated w1:p1
// or delete the first's records as closed panes.
func sessionKey(id identity) string {
	sum := sha1.Sum([]byte(id.pyJSON()))
	return hex.EncodeToString(sum[:])[:12]
}

// stateDir is this session's state directory: sessions/<key>/ in the
// plugin's. anythingSnoozed must agree on where state can live: every
// sessions/*/, plus 0.1.0's single file at the top (see recoverLegacy).
func stateDir() string {
	return filepath.Join(pluginStateDir(), "sessions", sessionKey(sessionIdentity("")))
}

// anythingSnoozed is the idle check every hook makes first: snooze deletes
// its state file whenever nothing is snoozed, so no file anywhere means
// nothing to do. Working out this session's key would cost more than looking
// in all of them.
func anythingSnoozed() bool {
	root := pluginStateDir()
	if _, err := os.Stat(filepath.Join(root, "snoozed.json")); err == nil {
		return true
	}
	found, _ := filepath.Glob(filepath.Join(root, "sessions", "*", "snoozed.json"))
	return len(found) > 0
}

// pruneStaleSessions removes the state of herdr sessions that no longer
// exist. A deleted session's directory would otherwise stay forever: its file
// keeps every hook in every session doing work. Its records are safe to drop:
// tokens and views die with the server that held them, and a recreated
// session of the same name is a different one (sessionKey).
func pruneStaleSessions() {
	root := filepath.Join(pluginStateDir(), "sessions")
	keys, err := os.ReadDir(root)
	if err != nil {
		return
	}
	mine := sessionKey(sessionIdentity(""))
	for _, k := range keys {
		if k.Name() == mine {
			continue
		}
		path := filepath.Join(root, k.Name())
		data, err := os.ReadFile(filepath.Join(path, "session.json"))
		if err != nil {
			// No identity (yet — it may be mid-creation) or can't read it:
			// unknown is never proof the session is gone.
			continue
		}
		var id identity
		if json.Unmarshal(data, &id) != nil || id.ID == "" {
			continue // no ID to compare
		}
		if st, err := os.Stat(filepath.Dir(id.Socket)); err == nil && st.IsDir() && readSessionID(id.Socket) == id.ID {
			continue // alive (running or just stopped)
		}
		// Its directory is gone, or holds another incarnation's ID. Take its
		// lock, so nothing is mid-write; if it's busy, it isn't stale.
		lock, err := os.OpenFile(filepath.Join(path, "lock"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			continue
		}
		if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			logf("removing state of a herdr session that no longer exists: %s", k.Name())
			os.RemoveAll(path)
		}
		lock.Close()
	}
}

// runningSessions is the sockets of every running herdr session, or nil and
// false if herdr can't say (then nothing may be assumed about the others).
func runningSessions() ([]string, bool) {
	bin := os.Getenv("HERDR_BIN_PATH")
	if bin == "" {
		bin = "herdr"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "session", "list", "--json").Output()
	if err != nil {
		return nil, false
	}
	var res struct {
		Sessions []struct {
			Running    bool   `json:"running"`
			SocketPath string `json:"socket_path"`
		} `json:"sessions"`
	}
	if json.Unmarshal(out, &res) != nil {
		return nil, false
	}
	var socks []string
	for _, s := range res.Sessions {
		if s.Running && s.SocketPath != "" {
			socks = append(socks, s.SocketPath)
		}
	}
	return socks, true
}

// inSession acts as the herdr session behind sock: its state, its socket.
func inSession(sock string, f func()) {
	old, had := os.LookupEnv("HERDR_SOCKET_PATH")
	os.Setenv("HERDR_SOCKET_PATH", sock)
	defer func() {
		if had {
			os.Setenv("HERDR_SOCKET_PATH", old)
		} else {
			os.Unsetenv("HERDR_SOCKET_PATH")
		}
	}()
	f()
}

const (
	legacyRetry    = 30 * time.Second
	legacyRetryMax = time.Hour
)

// recoverLegacy runs after upgrading from 0.1.0, which kept one state file
// for every session at the top of the plugin's state directory. Its records
// may belong to any session, so none adopts them: instead every running
// session fails open. For each one whose own state is idle, anything of ours
// still in it (our view, a token with no record) came from 0.1.0 — cleared,
// so agents reappear rather than stay hidden by a record no session will read
// again. Done through each session's own socket, by whichever session ticks
// first, so a quiet session isn't left out. A session that isn't running
// needs nothing: its view and tokens died with its server.
//
// The old file goes only once every running session has recovered, with
// every token clear confirmed; until then (herdr unreachable, a session
// refusing), it stays. One process recovers at a time (the others skip
// straight past), and after a failure the next attempt waits, doubling from
// legacyRetry up to legacyRetryMax: a refusing session must not turn every
// hook into seconds of retries.
func recoverLegacy(cfg config) {
	root := pluginStateDir()
	legacy := filepath.Join(root, "snoozed.json")
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	retryPath := filepath.Join(root, "legacy-retry.json")
	var retry struct {
		At    float64 `json:"at"`
		Delay float64 `json:"delay"`
	}
	if data, err := os.ReadFile(retryPath); err == nil && json.Unmarshal(data, &retry) == nil {
		if float64(time.Now().UnixMilli())/1000 < retry.At {
			return // backing off after a failure
		}
	} else {
		retry.Delay = 0
	}
	lock, err := os.OpenFile(filepath.Join(root, "legacy.lock"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return // another process is recovering right now
	}
	if recoverEverySession(cfg) {
		os.Remove(legacy)
		os.Remove(retryPath)
		return
	}
	delay := min(legacyRetryMax.Seconds(), max(legacyRetry.Seconds(), retry.Delay*2))
	next, _ := json.Marshal(map[string]float64{"at": float64(time.Now().UnixMilli())/1000 + delay, "delay": delay})
	_ = os.WriteFile(retryPath, next, 0o600)
}

// recoverEverySession is whether every running session is known to be
// recovered.
func recoverEverySession(cfg config) bool {
	socks, complete := runningSessions()
	mine := realPath(socketPath())
	listed := false
	for _, s := range socks {
		listed = listed || realPath(s) == mine
	}
	if !listed {
		socks = append(socks, socketPath())
	}
	for _, sock := range socks {
		inSession(sock, func() {
			marker := filepath.Join(stateDir(), "legacy-recovered")
			if _, err := os.Stat(marker); err == nil {
				return
			}
			err := withState(func(st *state) error {
				if st.idle() {
					st.View = recoverView()
				}
				_, err := syncState(st, cfg, syncOpts{strict: true})
				return err
			})
			if err == nil {
				err = os.WriteFile(marker, []byte("0.1.0 state recovered\n"), 0o600)
			}
			if err != nil {
				logf("0.1.0 recovery not done yet in %s - %v", sock, err)
				complete = false
				return
			}
			logf("recovered from 0.1.0 in %s", sock)
		})
	}
	return complete
}

// pidText is a pid as a file holds it.
func pidText(pid int) string { return strconv.Itoa(pid) }
