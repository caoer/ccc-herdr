# ccc-herdr

Herdr companion plugin for the ccc fleet family.

Press the bound key and a popup lists every live pane, agents first. Type to
fuzzy-search across the ccc session id, agent name, role, session, terminal
title, workspace and tab labels, and working directory. Enter jumps to the
selected pane — herdr switches workspace and tab as needed. Escape dismisses.

## Install

```sh
herdr plugin install caoer/ccc-herdr --yes
```

Installing builds the binary from source, so a Go toolchain (1.24+) must be on
`PATH`. For a local checkout:

```sh
go build -o bin/ccc-herdr .
herdr plugin link /path/to/ccc-herdr
```

## Keybinding

```toml
[[keys.command]]
key = "prefix+f"
type = "plugin_action"
command = "ccc-herdr.jump"
description = "jump to agent"
```

Reload with `herdr server reload-config`.

## Headless use

```sh
ccc-herdr list            # print the candidate table
ccc-herdr focus ad3009b4  # jump straight to the best match
```

Both read the same live snapshot the popup uses, so agents can drive the jump
without a UI.

## Painter

The resident painter renders every ccc pane label (identity tokens, navigator
title, AUQ blocked label) from statusd's fact files. ccc-statusd no longer
speaks to herdr. Architecture and cutover: `DESIGN.md`.

```sh
ccc-herdr check [sid]     # config diagnostics + exact wire lines (exit 1 on diags)
ccc-herdr paint [sid]     # force a repaint now
ccc-herdr painter start   # detach the resident loop; no-op if one is up
ccc-herdr painter restart # stop the incumbent (pid in the lock), then start
ccc-herdr painter run     # the resident loop, foreground
```

Lifecycle is plugin-owned: the `[[startup]]` hook in `herdr-plugin.toml` runs
`painter start` once after herdr restores the session and again on live
handoff, so an installed+enabled plugin paints on a fresh host with nothing
hand-started. `painter start` detaches (setsid, log to
`$UCC_HOME/cache/ccc-herdr/painter.log`) and is idempotent through the flock
singleton. herdr does not supervise it: a crashed painter stays down until the
next herdr start or a hand `painter start`. `contrib/dev.ccc.herdr-painter.plist`
adds macOS launchd KeepAlive supervision on top; the flock keeps both safe.

Upgrading the plugin does NOT upgrade the running painter: the old process
holds the flock, so a fresh binary's `painter start` no-ops and the host keeps
painting with the old code, silently. Finish an upgrade with `painter restart`
— it reads the pid the incumbent stamped in `painter.lock`, SIGTERMs it, waits
for the lock, then starts the new one. (On the mac, launchd KeepAlive respawns
whatever the plist points at — restart the job instead.)

Config: `$UCC_HOME/config/ccc-herdr.star`, else `ccc-herdr.toml`, hot-reloaded
on save. Missing file = built-in defaults (the classic id/session/role/name
row) — panes still paint. `ccc-herdr check` prints the resolved path and marks
it `(missing — built-in defaults)` so an unconfigured host is not mistaken for
a broken one.
