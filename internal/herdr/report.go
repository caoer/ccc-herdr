package herdr

// Metadata reporting: the wire half of the painter, ported from
// ccc-statusd/internal/herdr (P2 — statusd sheds it). Display-only by
// contract: every path is best-effort and time-bounded, a failed send never
// affects any session.

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

const (
	dialTimeout = 300 * time.Millisecond
	ioDeadline  = 500 * time.Millisecond

	fetchReadDeadline = 250 * time.Millisecond
	// fetchResponseCap caps the pane.get response read: a pane_info line is a
	// few KB; anything larger means the socket is not herdr.
	fetchResponseCap = 64 * 1024

	// maxValueLen is herdr's token-value limit.
	maxValueLen = 80
)

// Report is one pane metadata request (default method pane.report_metadata).
// Zero-valued fields are omitted from the wire. A nil value inside Tokens
// clears that token on the pane.
type Report struct {
	PaneID string
	Source string
	Agent  string
	// Seq orders same-source reports on the herdr side. 0 = stamped at send
	// time. Callers that defer the send MUST stamp it at decision time, or a
	// delayed older snapshot outranks a newer one.
	Seq              int64
	TTLms            int64
	Tokens           map[string]any
	DisplayAgent     string
	StateLabels      map[string]string
	ClearStateLabels bool
	// Method overrides the JSON-RPC method — a herdr rename is a config edit.
	Method string
	// Extra is merged into params verbatim BEFORE the typed fields, so config
	// can add fields herdr grows but never shadow the addressing.
	Extra map[string]any
}

// ClampValue trims s and caps it at herdr's 80-char token-value limit.
func ClampValue(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxValueLen {
		s = string(r[:maxValueLen])
	}
	return s
}

// wireParams builds the JSON-RPC params. seq == 0 omits the seq key — the
// ContentHash form, so two composes of identical content hash equal.
func (r Report) wireParams(seq int64) map[string]any {
	params := map[string]any{}
	for k, v := range r.Extra {
		params[k] = v
	}
	params["pane_id"] = r.PaneID
	params["source"] = r.Source
	if seq != 0 {
		params["seq"] = seq
	}
	if r.Agent != "" {
		params["agent"] = r.Agent
	}
	if r.TTLms > 0 {
		params["ttl_ms"] = r.TTLms
	}
	if r.Tokens != nil {
		params["tokens"] = r.Tokens
	}
	if r.DisplayAgent != "" {
		params["display_agent"] = r.DisplayAgent
	}
	if r.StateLabels != nil {
		params["state_labels"] = r.StateLabels
	}
	if r.ClearStateLabels {
		params["clear_state_labels"] = true
	}
	return params
}

// WireMethod is Method or the default.
func (r Report) WireMethod() string {
	if r.Method != "" {
		return r.Method
	}
	return "pane.report_metadata"
}

// ContentHash digests everything herdr would receive except the seq.
// json.Marshal sorts map keys, so the digest is deterministic.
func (r Report) ContentHash() string {
	line, err := json.Marshal(map[string]any{
		"method": r.WireMethod(),
		"params": r.wireParams(0),
	})
	if err != nil {
		return fmt.Sprintf("marshal-error:%v", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(line))
}

// WireLine renders the full JSON-RPC request line, stamping a fresh seq when
// the report carries none. Exported for `ccc-herdr check`.
func WireLine(r Report) ([]byte, error) {
	if r.PaneID == "" || r.Source == "" {
		return nil, fmt.Errorf("herdr: PaneID and Source are required")
	}
	seq := r.Seq
	if seq == 0 {
		seq = time.Now().UnixNano()
	}
	return json.Marshal(map[string]any{
		"id":     fmt.Sprintf("%s:%d", r.Source, seq),
		"method": r.WireMethod(),
		"params": r.wireParams(seq),
	})
}

// SendReport delivers one report, bounded sub-second. The response read is
// best-effort — a missing reply is still success — but an explicit error
// response surfaces, so a rejected report is never mistaken for delivered.
func SendReport(socketPath string, r Report) error {
	line, err := WireLine(r)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ioDeadline))
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return err
	}
	ack := make([]byte, 4096)
	n, _ := conn.Read(ack)
	if n > 0 {
		var resp struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(ack[:n], &resp) == nil && len(resp.Error) > 0 {
			return fmt.Errorf("herdr rejected report: %s", resp.Error)
		}
	}
	return nil
}

// FetchPaneTitle reads the pane's stripped terminal title via pane.get — the
// {{HERDR_TITLE}} var, a pane fact only herdr knows. Best-effort and bounded;
// any failure returns ok=false so callers keep the last-known value.
func FetchPaneTitle(socketPath, paneID string) (title string, ok bool) {
	req, err := json.Marshal(map[string]any{
		"id":     fmt.Sprintf("ccc-herdr:pane-get:%d", time.Now().UnixNano()),
		"method": "pane.get",
		"params": map[string]any{"pane_id": paneID},
	})
	if err != nil {
		return "", false
	}
	conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(fetchReadDeadline))
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return "", false
	}
	// LimitReader makes the cap real; a truncated read fails Unmarshal below,
	// the intended degradation.
	line, err := bufio.NewReader(io.LimitReader(conn, fetchResponseCap)).ReadBytes('\n')
	if len(line) == 0 && err != nil {
		return "", false
	}
	var resp struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			Pane struct {
				TerminalTitleStripped string `json:"terminal_title_stripped"`
			} `json:"pane"`
		} `json:"result"`
	}
	if json.Unmarshal(line, &resp) != nil || len(resp.Error) > 0 {
		return "", false
	}
	return resp.Result.Pane.TerminalTitleStripped, true
}

// SocketIdentity stats the herdr socket file. herdr binds its socket fresh on
// start, so a changed inode = a restarted server whose metadata is gone.
func SocketIdentity(path string) (dev, ino uint64, ok bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	st, isUnix := info.Sys().(*syscall.Stat_t)
	if !isUnix {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}
