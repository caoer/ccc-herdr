package herdr

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
)

var requestCounter atomic.Int64

// Pane is one pane from session.snapshot. Agent-hosting panes carry the
// detection fields and the ccc identity tokens reported by ccc-statusd.
type Pane struct {
	PaneID        string            `json:"pane_id"`
	TabID         string            `json:"tab_id"`
	WorkspaceID   string            `json:"workspace_id"`
	Cwd           string            `json:"cwd"`
	ForegroundCwd string            `json:"foreground_cwd"`
	Focused       bool              `json:"focused"`
	Agent         string            `json:"agent"`
	DisplayAgent  string            `json:"display_agent"`
	AgentStatus   string            `json:"agent_status"`
	TerminalTitle string            `json:"terminal_title_stripped"`
	Tokens        map[string]string `json:"tokens"`
}

// AgentSeq carries the per-agent activity counter used for recency ordering.
type AgentSeq struct {
	PaneID string `json:"pane_id"`
	Seq    int    `json:"state_change_seq"`
}

type Tab struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
}

type Workspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

// Snapshot is the session.snapshot payload, reduced to what the jumper needs.
type Snapshot struct {
	Panes         []Pane      `json:"panes"`
	Agents        []AgentSeq  `json:"agents"`
	Tabs          []Tab       `json:"tabs"`
	Workspaces    []Workspace `json:"workspaces"`
	FocusedPaneID string      `json:"focused_pane_id"`
}

// Client talks newline-delimited JSON to the herdr server socket.
type Client struct {
	socketPath string
}

func NewClient() (*Client, error) {
	path := os.Getenv("HERDR_SOCKET_PATH")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve herdr socket: %w", err)
		}
		path = filepath.Join(home, ".config", "herdr", "herdr.sock")
	}
	return &Client{socketPath: path}, nil
}

type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) call(method string, params any, result any) error {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("%s: connect %s: %w", method, c.socketPath, err)
	}
	defer conn.Close()

	payload, err := json.Marshal(request{
		ID:     fmt.Sprintf("ccc-herdr-%d-%d", os.Getpid(), requestCounter.Add(1)),
		Method: method,
		Params: params,
	})
	if err != nil {
		return fmt.Errorf("%s: encode: %w", method, err)
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("%s: send: %w", method, err)
	}

	var decoded response
	if err := json.NewDecoder(conn).Decode(&decoded); err != nil {
		return fmt.Errorf("%s: read: %w", method, err)
	}
	if decoded.Error != nil {
		return fmt.Errorf("%s: %s (%s)", method, decoded.Error.Message, decoded.Error.Code)
	}
	if result != nil {
		if err := json.Unmarshal(decoded.Result, result); err != nil {
			return fmt.Errorf("%s: decode result: %w", method, err)
		}
	}
	return nil
}

// Snapshot fetches the live session snapshot.
func (c *Client) Snapshot() (*Snapshot, error) {
	var result struct {
		Snapshot *Snapshot `json:"snapshot"`
	}
	if err := c.call("session.snapshot", map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result.Snapshot, nil
}

// FocusPane focuses the pane, switching workspace and tab as needed.
func (c *Client) FocusPane(paneID string) error {
	return c.call("pane.focus", map[string]any{"pane_id": paneID}, nil)
}

// Notify shows a toast; failures are ignored.
func (c *Client) Notify(title, body string) {
	params := map[string]any{"title": title, "body": body, "sound": "none"}
	_ = c.call("notification.show", params, nil)
}
