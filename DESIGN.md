# P2 — the resident painter

ccc-herdr owns all herdr presentation for the ccc fleet. ccc-statusd owns
facts and never speaks to herdr. Decisions locked 2026-08-02 (ZT): painter is
a resident process; config is a plugin-owned file, the statusd `[herdr]`
table deprecates.

## Contract

The painter reads facts from files, all under `$UCC_HOME`:

| Fact | Source |
| -- | -- |
| status, title, model, context %, cost, profile, pane binding, auq_pending | `cache/ccc-status/<sid>.json` (statusd session cache) |
| session dir, agent file | `cache/ccc-cli/session-map.json` (ccc-cli) |
| role | agent-file frontmatter, `last_known_role` cache fallback |
| pane terminal title (`{{HERDR_TITLE}}`) | herdr `pane.get`, 5s throttle |

`auq_pending` is the one statusd addition: a generic count stamped on AUQ
edges and sweeps. Everything else already exists.

## Painter

`ccc-herdr painter run` (flock singleton) — started by the plugin's own
`[[startup]]` hook via `ccc-herdr painter start`, which detaches and exits.
herdr runs startup hooks once after session restore and again on live handoff,
asynchronously, reading their stdout/stderr to EOF — hence detach with a log
FILE, never an inherited pipe. herdr does not supervise: crash recovery is the
next herdr start, or launchd KeepAlive on the mac (`contrib/*.plist`).

One painter per HOST, not per herdr session: the statusd cache is host-global
and every session has its own herdr server. So each binding is sent to — and
judged for liveness by — the socket in its OWN cache entry (`herdr_socket_path`),
one snapshot per socket per sweep. `$HERDR_SOCKET_PATH` names only the
reconnect probe's endpoint, re-resolved to a reachable socket when the session
that launched the painter goes away.

- fsnotify on the cache dir → 300ms debounce per session → repaint
- fsnotify on the config file → reload → repaint all
- 60s sweep → lease renewal (identity TTL half-life, AUQ lease), decay
- a held herdr connection (events.subscribe) as liveness probe: reconnect
  after herdr restart → repaint all (herdr drops metadata on restart)

Dedup is in-memory (the statusd sidecar files existed only because hook
writers were short-lived processes). Painter start = repaint all, cheap.

Writers keep statusd's sources — `ccc:identity` (tokens, display_agent,
params) and `ccc:auq` (state_labels{blocked} lease) — so ownership transfers
without a herdr-side change.

## Config

`$UCC_HOME/config/ccc-herdr.star` when it exists, else
`$UCC_HOME/config/ccc-herdr.toml` (override: `CCC_HERDR_CONFIG`, format by
extension). Missing file = built-in defaults — panes still paint, and both
`check` and the painter's startup line print the resolved path marked
`(missing — built-in defaults)`. A broken config degrades to defaults loudly
(`ccc-herdr check` prints the same diagnostics the painter logs). The painter re-resolves the path on config-dir events, so authoring
the .star next to a live .toml switches formats without a restart.

TOML: static tables plus `{{VAR}}` templates — the statusd vocabulary plus
cache-derived additions (`STATUS`, `CONTEXT_PERCENT`, `CONTEXT_TOKENS`,
`COST`, `IDLE`). Starlark: `painter`/`auq` stay static dicts (same keys,
same resolver, same diagnostics); one `render(v)` function computes the
per-pane surfaces (`display_agent`, `tokens`, `params`) from the same facts
as struct attributes — conditionals (per-role tokens, title omission) live
in the config, not in new Go vars. Render is step-bounded and a render
error skips that pane's identity report loudly, never the fleet's.

Session-scope overrides (per-session table beating global) are deferred.

## Cutover sequencing

1. statusd (additive): stamp `auq_pending` into the session cache; rebuild,
   restart daemon.
2. Author `ccc-herdr.toml` from the live `[herdr]` table.
3. `[herdr] enabled = false` in the global hooks.toml — statusd writers off,
   labels persist under their TTL.
4. Start the painter (launchd), verify every pane repaints.
5. statusd sheds `internal/herdr` + `herdr_*.go` + the `herdr` CLI verb
   (pane-binding stamping in the hook path STAYS — it is a fact). Docs move:
   ccc-base HOOKS.md § [herdr] → pointer here.
