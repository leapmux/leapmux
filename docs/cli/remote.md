# `leapmux remote` — Remote-Control CLI

`leapmux remote` is a first-class command-line and SDK surface for driving
LeapMux from outside the browser. It exists for two callers with very
different threat models:

- **External users** running the CLI from their laptop against a hub (or a
  local socket). They authorize once via OAuth-style flows and persist a
  bearer token in `~/.config/leapmux/remote/<hub-host>.json` (mode 0600).
- **Spawned agents** invoking `leapmux remote ...` from inside their
  working directory. The worker injects `LEAPMUX_REMOTE_*` env vars at
  spawn time so the agent talks to a per-agent local IPC socket — no
  user session cookie ever enters env vars or `/proc/N/environ`.

This document is a quick reference. The full design rationale lives in
the original plan document (search the project plans dir for
"`leapmux-remote-cli-for-remote-control-of-leapmux.md`").

---

## Authentication

External-user flow:

```sh
leapmux remote auth login --hub https://my-hub        # PKCE local-redirect
leapmux remote auth login --hub https://my-hub --device-code   # RFC 8628
leapmux remote auth list
leapmux remote auth status --hub https://my-hub
leapmux remote auth logout --hub https://my-hub
```

Headless service accounts (admin only):

```sh
leapmux admin api-token issue --user <id> --client-name "ci-runner"
# prints access_token + refresh_token once; capture both immediately.
leapmux admin api-token list
leapmux admin api-token revoke --id <token-id>
```

Worker-spawned (no `--hub` flag needed): the env vars
`LEAPMUX_REMOTE_SOCK`, `LEAPMUX_REMOTE_TOKEN`, `LEAPMUX_REMOTE_USER_ID`,
`LEAPMUX_REMOTE_ORG_ID`, `LEAPMUX_REMOTE_WORKER_ID`,
`LEAPMUX_REMOTE_TAB_ID` + `LEAPMUX_REMOTE_TAB_TYPE` (one of `agent` /
`terminal`), `LEAPMUX_REMOTE_WORKING_DIR`, and (for agents only)
`LEAPMUX_REMOTE_AGENT_PROVIDER` are set automatically. Every variable
that carries an entity identifier uses the `_ID` suffix so scripts
can `printenv | grep LEAPMUX_REMOTE_.*_ID` to list every id in the
spawn's context. Terminals only get them when opened with
`--remote-enabled`.

## Commands

All commands emit `{"data": ...}` on success and
`{"error": {"code": "...", "message": "..."}}` on failure. Both
envelopes go to **stdout** so `leapmux remote ... | jq` works
uniformly; failure is signalled by a non-zero exit code, not a
separate output stream. Diagnostic logs (warnings, debug) go to
stderr — script consumers can ignore stderr without losing
visibility into the error itself.

```sh
# Check the result envelope alongside the exit code:
leapmux remote agent send --tab-id "$id" --message "hi" \
  | jq -e '.error // empty' >&2
```

### Universal entity-ID flags

Every command that consumes any of `{workspace, tile, worker, org,
user, tab, working_dir}` accepts any sufficient combination of:

- `--tab-id` (with optional `--tab-type` for the generic `tab`
  group; agent/terminal groups pin the type)
- `--tile-id`
- `--workspace-id`
- `--worker-id`
- `--org-id`
- `--user-id`

The hub resolves the missing fields via `LocateTab` (tab → workspace,
tile, worker), `LocateTile` (tile → workspace, org), `GetWorkspace`
(workspace → org, user), `GetWorker` (worker → user, org), and
`GetUser` (user → org). Tab type can be omitted when only the tab id
is given — the hub treats `TAB_TYPE_UNSPECIFIED` as a wildcard and
returns the matched type.

Conflicting inputs are rejected up front. For example, passing
`--tab-id` (whose owning workspace is A) together with
`--workspace-id B` returns an `invalid_request` envelope listing
both flags and their disagreeing values; nothing is sent to the
hub.

`tab list` is the one exception to the type-constraint rule:
`--tab-type` there is an *output filter*, not a derivation
constraint. `leapmux remote tab list --tab-type agent` lists every
agent tab in the resolved org / workspace even when the spawn's
`LEAPMUX_REMOTE_TAB_ID` points at a terminal — the resolver passes
`TAB_TYPE_UNSPECIFIED` to `LocateTab` so the env-defaulted tab id
auto-detects, and the filter is applied to the response.

Worker-spawned scripts get every relevant ID through env vars:
`LEAPMUX_REMOTE_TAB_ID`, `_TAB_TYPE`, `_WORKER_ID`, `_ORG_ID`,
`_USER_ID`, `_WORKING_DIR` (see the table at the end of this doc).
Inside an agent the most-common `tab open` invocation is therefore
just `leapmux remote tab open --type=agent` — the parent's
workspace, tile, and worker are inherited via `LEAPMUX_REMOTE_TAB_ID`.

### Workspaces & tabs

```sh
leapmux remote workspace list [--org-id <id>]
leapmux remote workspace get --workspace-id <id>
leapmux remote workspace create --org-id <id> --title "..."
leapmux remote workspace rename --workspace-id <id> --title "..."
leapmux remote workspace delete --workspace-id <id> [--force]

leapmux remote tab list [--workspace-id <id>] [--org-id <id>] [--tab-type agent|terminal|file]
leapmux remote tab get --tab-id <id> [--workspace-id <id>] [--tab-type agent|terminal|file]
leapmux remote tab open --type agent|terminal|file [--workspace-id <id>] [--tile-id <id>] [--worker-id <id>] [placement] ...
leapmux remote tab close --tab-id <id> [--force]
leapmux remote tab rename --tab-id <id> --title "..."
leapmux remote tab move --tab-id <id> [--target-tile-id <dest>] [--target-workspace-id <dest>] [placement]
```

`[placement]` is one of `--first`, `--last`, `--before <tab-id>`, or
`--after <tab-id>`; the four flags are mutually exclusive and the
default is `--last`. `--before` / `--after` take a tab id — not a
LexoRank — and the CLI computes the rank against the sibling tabs
already on the destination tile. The destination tile defaults to the
referenced tab's own tile when `--before` / `--after` is used; pass
`--tile-id` / `--target-tile-id` if you want to assert it explicitly.

#### Self-target guard

`workspace delete`, `tab close`, `tile close`, and `tile remove-grid`
refuse to run when the calling tab's PTY would be torn down by the
operation itself — i.e. the resolved target is (or contains) the tab
identified by `LEAPMUX_REMOTE_TAB_ID`. Without the guard the command's
own response would be cut off mid-emit when its host shell is killed.

The guard only fires when the operation actually destroys the
calling tab:

- `workspace delete` and `tab close` always destroy. Self-target →
  guard fires.
- `tile close --with-tabs=close` and `tile remove-grid --with-tabs=close`
  tombstone tabs. Self-target → guard fires.
- `tile close --with-tabs=move` and `tile remove-grid --with-tabs=move`
  migrate the tab record's `tile_id` before the old tile is
  tombstoned. The tab (and its PTY) survive, so the guard is
  skipped and the command runs without `--force` even when the
  calling tab is in the target subtree.

Pass `--force` to override the destructive cases. The error envelope
uses `code=self_target_refused` so scripts can branch on it.

### Tile layout

```sh
leapmux remote tile list [--workspace-id <id>]
leapmux remote tile split --tile-id <id> [--direction vertical|horizontal]
leapmux remote tile make-grid --tile-id <id> --rows N --cols M
leapmux remote tile close --tile-id <id> [--with-tabs close|move] [--recursive] [--force]
leapmux remote tile remove-grid --tile-id <id> [--with-tabs close|move] [--force]
leapmux remote tile set-ratios --tile-id <id> --ratios r1,r2[,...]
leapmux remote tile set-grid-ratios --tile-id <id> [--row-ratios r1,r2,...] [--col-ratios c1,c2,...]
```

#### `--with-tabs` policy

`tile close` and `tile remove-grid` would silently destroy or
relocate tabs without an explicit choice, so they require
`--with-tabs` whenever the target (or, for `tile close --recursive` /
`tile remove-grid`, the subtree) has at least one live tab:

- **`close`** — tombstone every affected tab. Permanent.
- **`move`** — migrate tabs to a safe destination:
  - `tile close --with-tabs=move` sends tabs to the heir tile (the
    first leaf of the closest adjacent sibling subtree, matching the
    frontend's CloseTileDialog).
  - `tile remove-grid --with-tabs=move` collapses the grid back to a
    single tile **in the grid's old slot**: a fresh leaf is minted
    under the grid's parent (inheriting its position), every tab in
    the subtree migrates to that leaf, and the grid + cells are
    tombstoned. For a root grid, the root NodeRecord is kept alive
    and flipped from `GRID` to `LEAF` in place, with tabs migrated
    onto the root.

A tile with zero live tabs accepts neither flag — it just closes.

`tile make-grid` does **not** take `--with-tabs`: it always migrates
tabs to cell `[0,0]` (matching the frontend, which silently migrates
without prompting because the conversion isn't destructive). To get
a fresh empty grid, close the tabs first with `tab close` and then
run make-grid.

#### Cascade close (`--recursive`)

`tile close` on a SPLIT is rejected by default; `--recursive` opts
in and cascades the close to every descendant in one batch, applying
`--with-tabs` to every tab below.

`tile close` on a GRID — with or without `--recursive` — is rejected
unconditionally; use `tile remove-grid` instead (it's the grid-
specific verb and handles root grids correctly via the in-place
kind flip).

`tile close` on a **grid cell** (a leaf whose parent is a GRID) is
also rejected: closing one cell leaves an unfillable hole because
grid shape is fixed at `make-grid` time. To clear a cell, close its
tabs individually with `tab close`. To remove the whole grid, use
`tile remove-grid`.

#### Ratios input

`tile set-ratios`, `tile set-grid-ratios`, and any `ratios` /
`row_ratios` / `col_ratios` field inside a `layout set` tree accept
any comma-separated list of non-negative floats and rescale them so
the wire payload sums to exactly `1.0` (within float64 ULP). Both of
these are equivalent:

```sh
leapmux remote tile set-ratios --tile-id $T --ratios 0.25,0.75
leapmux remote tile set-ratios --tile-id $T --ratios 1,3
```

The CLI rejects empty lists, malformed numbers, negative weights,
NaN/Inf, and all-zero inputs before sending any RPC.

`tile set-ratios` also rejects when `--ratios` length doesn't match
the SPLIT's live child count, and `tile set-grid-ratios` rejects
when `--row-ratios` / `--col-ratios` length doesn't match the GRID's
`rows` / `cols`.

### Layout snapshots

```sh
leapmux remote layout get [--workspace-id <id>]
leapmux remote layout set {--file PATH | --stdin} [--workspace-id <id>]
```

`layout get` prints the projected tile tree plus a `tabs_by_tile`
index — the same data shape that `layout set` accepts as input.

`layout set` rewrites the entire workspace layout in a single batch:
the existing root NodeRecord is mutated in place to match the input
root, every other live descendant is tombstoned leaves-first, the new
subtree is synthesized under the kept root, and every live tab is
repointed onto the new tree's first leaf so the CRDT's "no orphaned
tabs" invariant holds.

Exactly one of `--file PATH` and `--stdin` must be supplied. `--stdin`
reads JSON from standard input so generators can pipe directly:

```sh
jq -n '{kind:"split",direction:"vertical",children:[{kind:"leaf"},{kind:"leaf"}]}' \
  | leapmux remote layout set --stdin
```

The input shape mirrors `layout get`'s output. Required fields per
kind:

- `leaf` — no other fields allowed.
- `split` — `direction` (`horizontal`/`vertical`), `children` (≥ 2),
  optional `ratios` of the same length. Ratios are normalized.
- `grid` — `rows` and `cols` (both 1..20), `children` of length
  `rows * cols`, optional `row_ratios` of length `rows`, optional
  `col_ratios` of length `cols`. Ratios are normalized.

The CLI validates the tree before opening any CRDT call and returns
a path-anchored error such as
`root.children[1].children[0]: SPLIT requires at least 2 children`
so a malformed subtree is easy to locate. The same validator runs at
every nesting level, so nested SPLIT / GRID nodes get the same checks
as the root.

#### Concurrent modification

Between the CLI's snapshot read and the commit, another client could
land a new tab on a tile the rewrite is about to tombstone — that
tab's `tile_id` would resolve to a dead leaf, which the hub's
`tabPlacementCheck` rejects atomically with
`BATCH_REJECTION_TAB_PLACEMENT_INVALID`. The CLI handles this
transparently: on rejection, it re-bootstraps the snapshot (picking
up the racing tab) and rebuilds the batch so the second attempt
includes a `tile_id` update for it.

If a second race during the retry also rejects the batch, the
command gives up and emits
`{"error":{"code":"concurrent_modification", ...}}` instead of the
opaque `batch_rejected` envelope. Scripts can branch on the code and
retry the whole command from scratch.

The success envelope includes an `attempts` field (1 on the common
path, 2 when a retry committed) so callers can spot races
post-hoc.

### Workers

```sh
leapmux remote worker list
leapmux remote worker get --worker-id <id>
leapmux remote worker pins list [--hub <url>]
leapmux remote worker pins show --worker-id <id> [--hub <url>]
leapmux remote worker pins remove --worker-id <id> [--hub <url>]
```

### Agents

The `agent` subgroup carries operations specific to agents
(send-message / interrupt-turn / set-settings / etc.); generic
open/close/list/rename go through the `tab` subgroup with
`--type=agent` (or via the resolver's tab-type inference).

```sh
leapmux remote agent send --tab-id <id> --message "..."           # or --stdin
leapmux remote agent interrupt --tab-id <id>
leapmux remote agent get --tab-id <id>
leapmux remote agent providers [--tab-id <id>] [--worker-id <id>]
leapmux remote agent messages --tab-id <id> [--follow]
leapmux remote agent set --tab-id <id> [--model <m>] [--effort <e>] ...
leapmux remote agent send-control-response --tab-id <id> --content "..."
```

### Terminals

Terminals are opt-in for remote control via `--remote-enabled` on
`tab open --type=terminal`. The terminal subgroup carries
type-specific verbs only.

```sh
leapmux remote terminal send --tab-id <id> --data "..."             # or --stdin
leapmux remote terminal get --tab-id <id>
leapmux remote terminal shells [--worker-id <id>]
```

### Events

```sh
leapmux remote events watch --workspace-id <id>     # JSON-lines on stdout
leapmux remote events watch --workspace-id <id> --include layout,private
```

`--include` filters which event sources are subscribed. Allowed values:

| Source     | Stream                                                       |
| ---------- | ------------------------------------------------------------ |
| `layout`   | Hub `OrgCRDT.WatchOrg` (`OrgMaterialized`, canonical ops)    |
| `private`  | Worker `WatchWorkspacePrivateEvents` (TabRenamed, FileTabPath)|
| `agent`    | Per-worker `WatchEvents` agent frames (`AgentChatMessage`)   |
| `terminal` | Per-worker `WatchEvents` terminal frames (`TerminalData`)    |

Both transports fan the per-worker streams out across every worker
hosting a tab in the current snapshot; new workers are picked up on
the next snapshot and tab-set deltas trigger a cursor-preserving
re-subscribe. Worker-spawned (local-IPC) mode reaches sibling workers
through the same per-agent socket — the worker's IPC router opens
cross-worker channels on the agent's behalf using a delegation
bearer scoped to the agent's workspace. A retention rollover on a
terminal stream emits a `{"source":"cursor_reset", ...}` notice line
so consumers can distinguish a fresh snapshot replay from a flood of
new events.

Default (flag omitted) is "include everything". Each emitted JSON line
carries a `source` key matching one of the values above so consumers
can branch without a follow-up filter.

## Environment variable summary

| Var                           | Set by      | Used by                          |
| ----------------------------- | ----------- | -------------------------------- |
| `LEAPMUX_HUB`                 | user        | `--hub` fallback                 |
| `LEAPMUX_REMOTE_CONFIG_DIR`   | user        | Override credential dir          |
| `LEAPMUX_REMOTE_SOCK`         | worker      | Local IPC URL                    |
| `LEAPMUX_REMOTE_TOKEN`        | worker      | Local IPC bearer                 |
| `LEAPMUX_REMOTE_USER_ID`      | worker      | Informational (authenticated user) |
| `LEAPMUX_REMOTE_ORG_ID`       | worker      | Default `--org-id`               |
| `LEAPMUX_REMOTE_WORKER_ID`    | worker      | Default `--worker-id`            |
| `LEAPMUX_REMOTE_TAB_ID`       | worker      | Default `--tab-id` (paired with `_TAB_TYPE`) |
| `LEAPMUX_REMOTE_TAB_TYPE`     | worker      | Discriminates `TAB_ID` (`agent` \| `terminal`) |
| `LEAPMUX_REMOTE_WORKING_DIR`  | worker      | Default `--working-dir` for new spawns |
| `LEAPMUX_REMOTE_AGENT_PROVIDER` | worker    | Default `--provider` for new agents |

## Threat model

- **External CLI** carries one bearer token (api_tokens row, hashed
  HMAC-SHA256 with a server pepper). E2EE channels to workers use the
  same Noise_NK handshake the frontend uses, with worker static keys
  pinned per-hub TOFU under `~/.config/leapmux/remote/<hub-host>/pins.json`.
- **Spawned agent** uses a per-agent local socket (mode 0600) plus a
  per-process `LEAPMUX_REMOTE_TOKEN`. The worker's local-IPC server
  scopes the bearer to the spawning user + workspace. When the agent
  closes, the socket is torn down and the token zeroed.
- Cross-worker calls from a spawned agent use a worker-minted
  delegation_tokens row, scoped to `(user_id, workspace_id)`. The
  delegation token is minted lazily on first cross-worker need (so it
  doesn't exist if the agent never reaches across workers) and revoked
  on agent close.

## Worker-spawned vs hub-bound parity

Every command works in both transports. A `leapmux remote` invocation
inside a worker-spawned agent (with `LEAPMUX_REMOTE_SOCK` set) routes
hub-bound RPCs (`workspace`, `tab`, `worker`, `layout`, `tile`) through
the worker's local-IPC server, which forwards them to the hub using
the per-agent delegation token. Inner-RPCs to a sibling worker
(`agent`, `terminal`, `file`, `git` against `--worker-id <other>`) go
through the same local-IPC server, which opens a Noise_NK channel to
the sibling worker on the agent's behalf. The CLI invocation looks
identical in both modes; only the transport differs.
