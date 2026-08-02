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
ccc-herdr painter run     # the resident loop (launchd: contrib/*.plist)
```

Config: `$UCC_HOME/config/ccc-herdr.toml`, hot-reloaded on save. Missing file
= built-in defaults (the classic id/session/role/name row).
