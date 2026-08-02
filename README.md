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

## Roadmap

- Phase 1 (this): fuzzy jump-to-agent.
- Phase 2: absorb the ccc-statusd pane-label rendering (`herdr paint`) for
  richer fleet rendering.
