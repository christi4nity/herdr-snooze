package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// herdrError is a refusal from the herdr server, as opposed to a transport
// failure.
type herdrError struct {
	Code    string
	Message string
}

func (e *herdrError) Error() string { return e.Code + ": " + e.Message }

func isHerdrCode(err error, code string) bool {
	var he *herdrError
	return errors.As(err, &he) && he.Code == code
}

func defaultSocket() string {
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), "herdr", "herdr.sock")
}

func socketPath() string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	return defaultSocket()
}

// herdrCall sends one request and decodes `result` into out. The server
// answers a single line per connection and hangs up.
func herdrCall(method string, params any, out any) error {
	conn, err := net.DialTimeout("unix", socketPath(), 5*time.Second)
	if err != nil {
		return fmt.Errorf("herdr socket: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if params == nil {
		params = map[string]any{}
	}
	req, err := json.Marshal(map[string]any{"id": "snooze", "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReaderSize(conn, 1<<16).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return fmt.Errorf("herdr %s: %w", method, err)
	}
	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &reply); err != nil {
		return fmt.Errorf("herdr %s: bad reply: %w", method, err)
	}
	if reply.Error != nil {
		code := reply.Error.Code
		if code == "" {
			code = "error"
		}
		return &herdrError{code, reply.Error.Message}
	}
	if out != nil && len(reply.Result) > 0 && string(reply.Result) != "null" {
		return json.Unmarshal(reply.Result, out)
	}
	return nil
}

func notify(title, body string) {
	params := map[string]any{"title": title, "body": nil}
	if body != "" {
		params["body"] = body
	}
	_ = herdrCall("notification.show", params, nil)
}

// pane is one entry of pane.list.
type pane struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	TerminalID  string `json:"terminal_id"`
	// Agent is the kind of agent herdr detects in the pane ("claude",
	// "codex"…), "" for none.
	Agent       string `json:"agent"`
	AgentStatus string `json:"agent_status"`
	Focused     bool   `json:"focused"`
	// Tokens are the pane's metadata tokens, by name: snooze's own and any
	// other plugin's.
	Tokens        map[string]any `json:"tokens"`
	Title         string         `json:"title"`
	TitleStripped string         `json:"terminal_title_stripped"`
	TerminalTitle string         `json:"terminal_title"`
	Cwd           string         `json:"cwd"`
}

func listPanes() ([]pane, error) {
	var res struct {
		Panes []pane `json:"panes"`
	}
	err := herdrCall("pane.list", nil, &res)
	sanitize(&res.Panes)
	return res.Panes, err
}

type workspaceInfo struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

func listWorkspaces() ([]workspaceInfo, error) {
	var res struct {
		Workspaces []workspaceInfo `json:"workspaces"`
	}
	err := herdrCall("workspace.list", nil, &res)
	sanitize(&res.Workspaces)
	return res.Workspaces, err
}

// viewOwner is who holds herdr's agent view, as a clear that owns nothing
// reports it.
type viewOwner struct {
	Active bool   `json:"active"`
	Source string `json:"source"`
	Label  string `json:"label"`
}

// probeView asks who holds the view: a clear whose source owns nothing
// changes nothing and reports the owner.
func probeView() (viewOwner, error) {
	var o viewOwner
	err := herdrCall("agent.view.clear", map[string]any{"source": source() + ".probe"}, &o)
	return o, err
}

func focusAgent(paneID string) error {
	return herdrCall("agent.focus", map[string]any{"target": paneID}, nil)
}
