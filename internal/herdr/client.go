package herdr

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
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

// NewClientFor targets one named socket. The painter is host-wide but herdr
// is not: every session has its own server, so liveness must be asked of the
// socket a pane actually belongs to, never of $HERDR_SOCKET_PATH alone.
func NewClientFor(socketPath string) *Client { return &Client{socketPath: socketPath} }

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

// Call timeouts: every socket path in this repo is bounded — an unbounded
// read here once latched the painter's sweep guard forever (a wedged herdr
// accepts and never answers). Snapshot responses run tens of KB, so the
// read deadline is looser than report.go's send path but still finite.
const (
	callDialTimeout = 300 * time.Millisecond
	callIODeadline  = 3 * time.Second
)

func (c *Client) call(method string, params any, result any) error {
	conn, err := net.DialTimeout("unix", c.socketPath, callDialTimeout)
	if err != nil {
		return fmt.Errorf("%s: connect %s: %w", method, c.socketPath, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(callIODeadline))

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

// SocketPath is the endpoint this client talks to — callers that scope
// per-socket decisions (the painter's pane-liveness veto) key on it.
func (c *Client) SocketPath() string { return c.socketPath }

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

// PaneRead returns the pane's terminal text. source is a wire value:
// visible, recent, or recent_unwrapped.
func (c *Client) PaneRead(paneID, source string, lines int) (string, error) {
	params := map[string]any{"pane_id": paneID, "source": source}
	if lines > 0 {
		params["lines"] = lines
	}
	var result struct {
		Read struct {
			Text string `json:"text"`
		} `json:"read"`
	}
	if err := c.call("pane.read", params, &result); err != nil {
		return "", err
	}
	return result.Read.Text, nil
}

// PluginPaneOpen opens a manifest pane entrypoint. placement overrides the
// manifest default when non-empty; targetPaneID seats split placements;
// workspaceID seats tab placements; env is merged into the pane's launch env.
func (c *Client) PluginPaneOpen(pluginID, entrypoint, placement, targetPaneID, workspaceID string, env map[string]string) error {
	params := map[string]any{"plugin_id": pluginID, "entrypoint": entrypoint, "focus": true}
	if placement != "" {
		params["placement"] = placement
	}
	if targetPaneID != "" {
		params["target_pane_id"] = targetPaneID
	}
	if workspaceID != "" {
		params["workspace_id"] = workspaceID
	}
	if len(env) > 0 {
		params["env"] = env
	}
	return c.call("plugin.pane.open", params, nil)
}

// Notify shows a toast; failures are ignored.
func (c *Client) Notify(title, body string) {
	params := map[string]any{"title": title, "body": body, "sound": "none"}
	_ = c.call("notification.show", params, nil)
}
