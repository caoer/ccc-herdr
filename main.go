// ccc-herdr is the herdr companion for the ccc fleet.
//
// Jump (phase 1):
//
//	ccc-herdr jump           action entrypoint: opens the jumper popup
//	ccc-herdr jumper         popup entrypoint: fuzzy finder over live panes
//	ccc-herdr focus <query>  headless: jump straight to the best match
//	ccc-herdr list           headless: print the candidate table
//
// Painter (phase 2):
//
//	ccc-herdr painter run    resident pane-label painter
//	ccc-herdr check [sid]    config diagnostics + the exact wire line(s)
//	ccc-herdr paint [sid]    force one repaint (all sessions without sid)
//
// Open file (phase 3):
//
//	ccc-herdr openfile       action entrypoint: opens the filepicker popup
//	ccc-herdr filepicker     popup entrypoint: fuzzy finder over file paths
//	                         scraped from the origin pane's output
//	ccc-herdr open-exec      opener-pane entrypoint: exec the editor on
//	                         CCC_HERDR_OPEN_FILE / CCC_HERDR_OPEN_LINE
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"github.com/caoer/ccc-herdr/internal/herdr"
	"github.com/caoer/ccc-herdr/internal/jump"
	"github.com/caoer/ccc-herdr/internal/openfile"
)

func main() {
	command := "jumper"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	var code int
	switch command {
	case "jump":
		code = runJump()
	case "jumper":
		code = runJumper()
	case "focus":
		code = runFocus(os.Args[2:])
	case "list":
		code = runList()
	case "painter":
		code = runPainter(os.Args[2:])
	case "check":
		code = runCheck(os.Args[2:])
	case "paint":
		code = runPaint(os.Args[2:])
	case "openfile":
		code = runOpenFile()
	case "filepicker":
		code = runFilePicker()
	case "open-exec":
		code = runOpenExec()
	default:
		fmt.Fprintln(os.Stderr, "usage: ccc-herdr jump|jumper|focus <query>|list|painter run|check [sid]|paint [sid]|openfile|filepicker|open-exec")
		code = 2
	}
	os.Exit(code)
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "ccc-herdr: %v\n", err)
	return 1
}

// runJump is the plugin action: it opens the jumper popup.
func runJump() int {
	bin := os.Getenv("HERDR_BIN_PATH")
	if bin == "" {
		bin = "herdr"
	}
	plugin := os.Getenv("HERDR_PLUGIN_ID")
	if plugin == "" {
		plugin = "ccc-herdr"
	}
	open := exec.Command(bin, "plugin", "pane", "open", "--plugin", plugin, "--entrypoint", "jumper")
	open.Stdout = os.Stdout
	open.Stderr = os.Stderr
	if err := open.Run(); err != nil {
		return fail(fmt.Errorf("open jumper popup: %w", err))
	}
	return 0
}

func candidates() (*herdr.Client, []jump.Candidate, error) {
	client, err := herdr.NewClient()
	if err != nil {
		return nil, nil, err
	}
	snap, err := client.Snapshot()
	if err != nil {
		return nil, nil, err
	}
	return client, jump.Build(snap, os.Getenv("HERDR_PANE_ID")), nil
}

// runJumper runs the fuzzy finder inside the popup and focuses the choice.
// CCC_HERDR_SELECT=<query> skips the TUI and jumps to the best match; the
// scripted path exists so the jump mechanics stay verifiable headlessly.
func runJumper() int {
	client, cands, err := candidates()
	if err != nil {
		return fail(err)
	}

	if query := os.Getenv("CCC_HERDR_SELECT"); query != "" {
		matches := jump.Filter(cands, query)
		if len(matches) == 0 {
			return fail(fmt.Errorf("no pane matches %q", query))
		}
		if err := client.FocusPane(cands[matches[0]].PaneID); err != nil {
			return fail(err)
		}
		return 0
	}

	final, err := tea.NewProgram(jump.New(cands)).Run()
	if err != nil {
		return fail(err)
	}
	if choice := final.(jump.Model).Choice(); choice != "" {
		if err := client.FocusPane(choice); err != nil {
			client.Notify("Jump to agent", err.Error())
			return fail(err)
		}
	}
	return 0
}

// runFocus jumps straight to the best match for the given query.
func runFocus(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ccc-herdr focus <query>")
		return 2
	}
	client, cands, err := candidates()
	if err != nil {
		return fail(err)
	}
	query := ""
	for _, a := range args {
		query += a + " "
	}
	matches := jump.Filter(cands, query)
	if len(matches) == 0 {
		return fail(fmt.Errorf("no pane matches %q", query))
	}
	target := cands[matches[0]]
	if err := client.FocusPane(target.PaneID); err != nil {
		return fail(err)
	}
	fmt.Printf("focused %s (%s %s %s)\n", target.PaneID, target.ID, target.Role, target.Title)
	return 0
}

const originPaneEnv = "CCC_HERDR_ORIGIN_PANE"

func pluginID() string {
	if id := os.Getenv("HERDR_PLUGIN_ID"); id != "" {
		return id
	}
	return "ccc-herdr"
}

func editor() string {
	for _, key := range []string{"CCC_HERDR_EDITOR", "EDITOR"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return "nvim"
}

// editorArgv builds the editor command line: editor [+line] file.
func editorArgv(file string, line int) []string {
	argv := []string{editor()}
	if line > 0 {
		argv = append(argv, fmt.Sprintf("+%d", line))
	}
	return append(argv, file)
}

// execEditor replaces this process with the editor, keeping the pane's PTY.
func execEditor(file string, line int) error {
	argv := editorArgv(file, line)
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("editor %q not found: %w", argv[0], err)
	}
	return syscall.Exec(path, argv, os.Environ())
}

// runOpenFile is the plugin action: it opens the filepicker popup, handing
// the focused pane through so the picker reads the right scrollback.
func runOpenFile() int {
	client, err := herdr.NewClient()
	if err != nil {
		return fail(err)
	}
	env := map[string]string{}
	if origin := os.Getenv("HERDR_PANE_ID"); origin != "" {
		env[originPaneEnv] = origin
	}
	if err := client.PluginPaneOpen(pluginID(), "filepicker", "", "", "", env); err != nil {
		return fail(fmt.Errorf("open filepicker popup: %w", err))
	}
	return 0
}

// runFilePicker runs the fuzzy finder inside the popup. Enter replaces the
// popup with the editor in place; ctrl+s / ctrl+t open the plugin's opener
// pane as a split beside the origin pane or as a new tab in its workspace.
func runFilePicker() int {
	client, err := herdr.NewClient()
	if err != nil {
		return fail(err)
	}
	snap, err := client.Snapshot()
	if err != nil {
		return fail(err)
	}
	origin := os.Getenv(originPaneEnv)
	if origin == "" {
		origin = snap.FocusedPaneID
	}
	cwd, workspace := "", ""
	for _, p := range snap.Panes {
		if p.PaneID != origin {
			continue
		}
		workspace = p.WorkspaceID
		cwd = p.ForegroundCwd
		if cwd == "" {
			cwd = p.Cwd
		}
		break
	}
	text, err := client.PaneRead(origin, "recent_unwrapped", 500)
	if err != nil {
		return fail(fmt.Errorf("read pane %s: %w", origin, err))
	}

	final, err := tea.NewProgram(openfile.New(openfile.Scan(text, cwd))).Run()
	if err != nil {
		return fail(err)
	}
	choice, mode := final.(openfile.Model).Choice()
	if choice == nil {
		return 0
	}

	switch mode {
	case openfile.OpenEdit:
		if err := execEditor(choice.Path, choice.Line); err != nil {
			client.Notify("Open file", err.Error())
			return fail(err)
		}
		return 0 // unreachable: exec replaced the process
	case openfile.OpenSplit, openfile.OpenTab:
		placement, target, ws := "split", origin, ""
		if mode == openfile.OpenTab {
			placement, target, ws = "tab", "", workspace
		}
		env := map[string]string{"CCC_HERDR_OPEN_FILE": choice.Path}
		if choice.Line > 0 {
			env["CCC_HERDR_OPEN_LINE"] = strconv.Itoa(choice.Line)
		}
		if err := client.PluginPaneOpen(pluginID(), "opener", placement, target, ws, env); err != nil {
			client.Notify("Open file", err.Error())
			return fail(err)
		}
	}
	return 0
}

// runOpenExec is the opener pane's command: exec the editor on the file the
// picker chose, delivered through the pane's launch env.
func runOpenExec() int {
	file := os.Getenv("CCC_HERDR_OPEN_FILE")
	if file == "" {
		return fail(fmt.Errorf("CCC_HERDR_OPEN_FILE is not set"))
	}
	line, _ := strconv.Atoi(os.Getenv("CCC_HERDR_OPEN_LINE"))
	if err := execEditor(file, line); err != nil {
		return fail(err)
	}
	return 0
}

// runList prints the candidate table, best-recency first.
func runList() int {
	_, cands, err := candidates()
	if err != nil {
		return fail(err)
	}
	for _, c := range cands {
		fmt.Printf("%-8s %-9s %-8s %-18s %-9s %-6s %-40s %s·%s\n",
			c.PaneID, c.ID, c.Role, c.Name, c.Status, c.Idle, c.Title, c.Workspace, c.Tab)
	}
	return 0
}
