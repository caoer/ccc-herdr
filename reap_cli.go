package main

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caoer/ccc-herdr/internal/herdr"
	"github.com/caoer/ccc-herdr/internal/reap"
)

// runReap is the plugin action: it opens the reaper popup.
func runReap() int {
	client, err := herdr.NewClient()
	if err != nil {
		return fail(err)
	}
	if err := client.PluginPaneOpen(pluginID(), "reaper", "", "", "", nil); err != nil {
		return fail(fmt.Errorf("open reaper popup: %w", err))
	}
	return 0
}

func idlePanes() (*herdr.Client, []reap.Idle, error) {
	client, err := herdr.NewClient()
	if err != nil {
		return nil, nil, err
	}
	snap, err := client.Snapshot()
	if err != nil {
		return nil, nil, err
	}
	return client, reap.Find(snap, client, reap.HasChildren), nil
}

// closeIfIdle re-checks the pane at the moment of closing: a list can be
// seconds old, and a pane that started a program since is refused.
func closeIfIdle(client *herdr.Client, paneID string) error {
	info, err := client.ProcessInfo(paneID)
	if err != nil {
		return err
	}
	if _, ok := reap.IsIdle(info, reap.HasChildren); !ok {
		return fmt.Errorf("%s is no longer idle; left open", paneID)
	}
	return client.ClosePane(paneID)
}

// runReaper runs the reaper TUI inside the popup.
func runReaper() int {
	client, panes, err := idlePanes()
	if err != nil {
		return fail(err)
	}
	model := reap.New(panes,
		func(id string) error { return closeIfIdle(client, id) },
		func(id string) (string, error) { return client.PaneReadANSI(id, "visible", 0) },
	)
	final, err := tea.NewProgram(model).Run()
	if err != nil {
		return fail(err)
	}
	if closed := final.(reap.Model).Closed(); len(closed) > 0 {
		client.Notify("Idle panes", fmt.Sprintf("closed %d: %s", len(closed), strings.Join(closed, " ")))
	}
	return 0
}

// runIdle prints the idle panes, or closes them all with --close.
func runIdle(args []string) int {
	closeAll := len(args) > 0 && args[0] == "--close"
	if len(args) > 0 && !closeAll {
		fmt.Fprintln(os.Stderr, "usage: ccc-herdr idle [--close]")
		return 2
	}
	client, panes, err := idlePanes()
	if err != nil {
		return fail(err)
	}
	code := 0
	for _, p := range panes {
		status := ""
		if closeAll {
			status = "closed"
			if err := closeIfIdle(client, p.PaneID); err != nil {
				status, code = "error: "+err.Error(), 1
			}
		}
		fmt.Printf("%-10s %-6s %-9s %-24s %s %s\n", p.PaneID, p.Shell, p.Seat, p.Location, p.Cwd, status)
	}
	return code
}
