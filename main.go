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
package main

import (
	"fmt"
	"os"
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"github.com/caoer/ccc-herdr/internal/herdr"
	"github.com/caoer/ccc-herdr/internal/jump"
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
	default:
		fmt.Fprintln(os.Stderr, "usage: ccc-herdr jump|jumper|focus <query>|list|painter run|check [sid]|paint [sid]")
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

// runList prints the candidate table, best-recency first.
func runList() int {
	_, cands, err := candidates()
	if err != nil {
		return fail(err)
	}
	for _, c := range cands {
		fmt.Printf("%-8s %-9s %-8s %-18s %-9s %-40s %s·%s\n",
			c.PaneID, c.ID, c.Role, c.Name, c.Status, c.Title, c.Workspace, c.Tab)
	}
	return 0
}
