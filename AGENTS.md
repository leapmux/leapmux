# LeapMux

Multi-agent coding assistant platform for nineteen agent providers.

- Backend: Go
- Frontend: SolidJS with vanilla-extract CSS (`.css.ts` files)
- E2E: Playwright
- Desktop: Tauri (Rust + Go sidecar)

## Project constraints

LeapMux is **pre-release**. Never raise backward compatibility, an upgrade
path, wire compatibility or migration hygiene, and never propose a shim.

- Edit `00001_initial.sql` in all three dialects in place; never add a
  migration file.
- Change a public interface whenever a better design wants it.
- Renumber, regroup, replace or remove protobuf fields freely, leaving **no
  `reserved` and no hole**: delete the field and move every later field down
  one. A reservation protects a number still meaningful on some wire or in some
  database, and nothing here is.
  - Renumbering an enum is a **data change** where its ordinals are stored (see
    **Enum columns store proto enum ordinals**): move the column `CHECK`s to a
    plain `BETWEEN 1 AND <last>`, no carve-out. `enum_column_numbering_test.go`
    states the range and asserts contiguity.
  - Not a hole: a header at `1` (sometimes `2`) and a payload `oneof` from
    `10`, as `frame.proto`, `user_ops.proto` and `worker.proto` do on purpose.

## Build system

Use `task` (`Taskfile.yaml`) targets, not the underlying tools. Package manager:
`bun` (`bun.lock`). Generators: `buf generate` (`task generate-proto`),
`scripts/generate-contracts.mjs` (`task generate-contracts`), `sqlc generate`
(`task generate-sqlc`).

**Run one `task` pipeline at a time.** Each runs `prepare-backend`, which
deletes `backend/internal/hub/generated/frontend/public` and copies
`frontend/.output/public` into it non-atomically, so a concurrent pipeline tests
a half-copied tree. It surfaces as about seventeen failures in `TestPolicy`,
`TestResolveFrontend` and siblings reading `does not contain "'sha256-"`,
because `hub` and `internal/hub/frontend` derive the Content Security Policy
from the inline `<script>` tags of the EMBEDDED `index.html`. Chain suites in
one command (`task test-backend; task test-frontend; task lint`). A starved run
also looks exactly like a hang, so a stacked run sends you after a deadlock that
is not there.

### Contracts: one source of truth across languages

A value used on BOTH sides of a language boundary (Go hub/CLI/worker and the TS
browser client) lives once, in `contracts/<name>.json`.
`task generate-contracts` validates it against its sibling `<name>.schema.json`,
runs the semantic cross-checks (derived arithmetic, enum coverage via
`buf build` descriptors, graph acyclicity), and emits Go to
`backend/generated/contracts/` and TS to `frontend/src/generated/contracts/`
(gitignored; CI generates before building).

- Each contract states what it holds and why in its own `_readme`. Read that,
  not a list here: a list here is a second copy, and the last one described 13
  of 31 contracts.
- `desktop.json` also emits a RUST module
  (`desktop/rust/src/generated/contracts.rs`, include!d from main.rs), so
  `prepare-desktop` depends on `generate-contracts`.
- Import generated code directly: Go `generated/contracts`, one spelling per
  constant (`contracts.MaxMessageSize`, `contracts.WSRouteChannel`); frontend
  `~/generated/contracts/*`. No re-export shims. `channelwire` keeps only
  Go-owned limits no contract holds (`WSReadLimit`, `UserEventsReadLimit`) plus
  wire helpers, and aliases no generated constant.
- Change a value by editing `contracts/<name>.json` and running
  `task generate`. A new proto enum value fails `generate-contracts` until its
  contract entry exists.
- Out of scope: values one side consumes (Go-only limits in `wire.go`, FE-only
  retry policies in the frontend) and dual-implemented ALGORITHMS, which stay
  differential — `testdata/*_conformance.json` (and `noise_rekey_vectors.json`)
  are the executable spec both suites replay.
- Contracts do not decide numbering: a single-side enum still takes a proto
  enum's numbering (see **Enum columns store proto enum ordinals**).

### JSON Schema validation

Every project-written JSON file validates against a JSON Schema via
`task validate-json`, which `task lint`, `task test` and `task test-no-docker`
run, so every CI OS job enforces it. `scripts/validate-json.mjs` sets scope:
`contracts/` and `testdata/` (root and package fixtures) resolve a SIBLING
`<name>.schema.json`; vendored syntax themes and license-override metadata use
the shared schemas stated on their rule. An in-scope file with no schema fails
hard. Tool-owned JSON (package.json, tsconfig, tauri configs, lockfiles) is out
of scope on purpose.

### vanilla-extract `.css.ts` files

Never write a bare `*.css.ts` basename inside a `.css.ts` file, even in a
comment: write `~/styles/global.css.ts` or `./widgets/SpanLines.css.ts`, never
`global.css.ts`. A bare basename fails the whole module with
`Styles were unable to be assigned to a file` at an unrelated line, and every
importing test then fails to load — which reads as a broken component, not a
broken comment.

### sqlc files

`backend/internal/hub/store/{sqlite,postgres,mysql}/db/queries/*.sql` and every
sqlc query file MUST be ASCII-only. A non-ASCII byte, usually in a comment,
fails the parser with a misleading `mismatched input 'SELECr'`-style error at
the wrong line. Use `--` and ASCII punctuation, never `—` or smart quotes.

## Common commands

- `task generate` — proto + sqlc generation
- `task build` — full build (backend + frontend)
- `task lint` — all linters (`task lint-backend` / `task lint-frontend` /
  `task lint-desktop`)
- `task test` — all tests (`task test-backend` / `task test-frontend`)
- `task test-e2e -- <files>` — only the affected E2E specs (a full run takes
  about half an hour)

Lint Rust/desktop with `task lint-desktop`, never `cargo clippy` directly: the
task first builds the Go sidecar that Tauri's bundle resources point at
(`../go/bin/*`), and without it clippy fails with a misleading build error.

## Coding conventions

### Enum columns store proto enum ordinals

A column whose values are a closed set is an enum: it takes its numbering from a
proto enum and stores the ordinal as an integer, never the name — even for a
hub-internal vocabulary, which goes in `proto/leapmux/v1/hub_storage.proto` (no
message or RPC) if it has no enum yet.

- **CHECK the range, starting at 1.** Proto3 fixes UNSPECIFIED at 0 and an unset
  Go field holds 0, so a forgotten write fails instead of storing a value nobody
  chose. Sole exception: `agents.goal_status`, where 0 means "no goal", as its
  column states.
- **Admit less than the enum declares** for a derived value or one another table
  owns — `AGENT_GOAL_STATUS_DORMANT` and the two `ControlResponseState` values
  for a request with no answer row, each with its reason at the column.
- **Bind the ordinal as a query parameter** from the Go constant, so a renumber
  propagates. Spell a literal only where a parameter cannot reach: a migration,
  or a partial-index predicate (SQLite matches those syntactically, so a bound
  `?` makes the index ineligible). `enum_column_numbering_test.go` in
  `hub/store` and `worker/db` pins each such literal, and a schema comment
  identifies its guard.
- **The Go domain type is a DEFINED type over the proto enum**
  (`type Status leapmuxv1.BackgroundTaskStatus`) — not an iota, not an alias —
  so the ordinals are one numbering, each conversion is a cast, and the type
  keeps its own methods.
- **A payload vocabulary is separate from storage.** Words sent in a
  notification payload or an RPC field keep their functions
  (`bgtask.StatusWire`, `agent.GoalStatusWire`, `oauth.ProviderTypeWire`,
  `store.AppRegistrationSourceWire`), each stating it is not the storage format.
  Add the inverse only for a caller that reads those words inward.

Why: otherwise one vocabulary is spelled in Go and in three SQL dialects with
nothing keeping them in step — a renamed constant left a
`status IN ('completed','failed',…)` list stale, and a `FromWire` falling
through to a default turned the drift into a plausible wrong value.

### Provider-specific logic belongs in the provider

Each provider is a Go package under `backend/internal/worker/agent/providers/`,
as each frontend plugin is a folder under `frontend/src/components/chat/providers/`.
These providers read their own native protocol: `claude`, `codex`, `copilot`,
`pi`, `zcode`, `codewhale`, `kimi`, `mimo`, `ohmypi`, `amp`, `cline`. These speak the
Agent Client Protocol on the shared base in `providers/acp` (`acp.Start`):
`opencode`, `cursor`, `kilo`, `goose`, `reasonix`, `qwen`, `grok`, `kiro`.
Copilot is NOT ACP — `providers/copilot/connection_test.go` and
`session_lifecycle_test.go` assert its arguments hold no `--acp`. MiMo Code is
a fork of OpenCode and is NOT ACP either: `mimo` drives MiMo's own HTTP server,
because MiMo's ACP adapter mixes a subagent's output into its parent's stream
and hides errors that only the event stream states. Oh My Pi (`ohmypi`) began
as a fork of Pi, but its RPC protocol differs from Pi's in its events, its
commands and its session files, so it shares no code with `pi`. Amp (`amp`)
streams JSON that resembles Claude Code's stream but is a protocol of its own,
so it shares no code with `claude`. Cline (`cline`) also offers an ACP mode, but
`cline` drives Cline's hub WebSocket protocol instead: only the hub carries
questions, plan approval, steering, the session list, images and reasoning
effort.

Anything depending on **one provider's wire format or message shapes** MUST live
in that provider's package, never in shared code (package `agent`,
`providers/internal/providerkit`, a shared `default*` function, a `switch` on
provider). One provider's shape in shared code breaks or half-serves the rest
and becomes a second source of truth.

- **Backend:** add a method to the `Provider` interface in
  `backend/internal/worker/agent/provider.go` (e.g.
  `IsSelfDisplayingControlTool`), implement it in each provider package, and
  dispatch through `Manager.Registry().Plugin(provider)`. `providers.Registrations()`
  lists every provider explicitly, and `bootstrap` builds the one `Registry`
  from that list.
- **The compiler enforces the boundary.** A provider imports `agent`, so `agent`
  cannot import a provider, and only `providers/**` can import
  `providers/internal/*`. `providers/imports_test.go` states the only edges
  between providers: the ACP family imports `acp`, Kilo builds on `opencode`,
  and OpenCode, Kilo, ZCode and MiMo Code read `opencode/opencodestore`.
  `TestShippedBinaryLinksNoTestSupport` (`cmd/leapmux`) keeps the test-support
  packages (`agenttest`, `acptest`, `claudetest`, …) out of the shipped binary.
- **Test a provider in its own package.** The shared suites live in
  `agent/agenttest` (with `acp/acptest` and `opencode/opencodetest`), and
  `providers/conformance_coverage_test.go` states which suites each provider
  package must run.
- **Frontend:** add a member to the capability of `ProviderPlugin` that owns it
  (`frontend/src/components/chat/providers/capabilities.ts`: `transcript`,
  `controls`, `session` or `configuration`; e.g. `configuration.effortGroupKey`
  beside `triggerModeGroupKey`) and implement it per plugin. `registry.ts` only
  registers and looks up plugins. A genuinely neutral shape (`{content}`,
  `{controlResponse}`) may share a `default*` helper; Anthropic/Codex/Pi/ACP
  parsing stays in its plugin.

### Backend provider package roles

Each concrete provider package uses these file names for the same roles.
Add a file only when the provider owns that role.

| File | Role |
|---|---|
| `agent.go` | The concrete agent type, state, and core runtime methods. |
| `start.go` | `Start` and startup setup. Include `var _ agent.StartFunc = Start`. |
| `registration.go` | `Registration`, the locator, and static provider metadata. |
| `catalog.go` | Model catalog data and conversion when they need a separate file. |
| `settings.go` | Live option methods, settings snapshots, and any shared axis table. |
| `control.go` | Control requests and replies. |
| `output.go` | Conversation output handlers and live output state. |
| `events.go` | Protocol event decoding and dispatch when these form one unit. |
| `rpc.go` | Request and reply transport, including response routing. |
| `session.go` | Session state and refresh logic when separate from core lifecycle. |
| `session_wire.go` | Session wire data when it differs from lifecycle code. |
| `session_lifecycle.go` | Session open, reset, and restore operations. |
| `stop.go` | Stop lifecycle and stop-window logic when separate from core lifecycle. |
| `subagent.go` | Child-agent state and transcript handling. |
| `connection.go` | Process connection ownership and setup. |
| `family.go` | Startup shared by providers in one protocol family. |
| `family_base.go` | Base behavior shared by that family. |
| `model_names.go` | Model ID normalization. |
| `launch_env.go` | Launch environment detection. |

Keep the locator and static fallback groups in `Registration()`.
The startup path reads that registration through `providerkit.ResolveLaunch`
or a shared protocol start. Do not copy the locator into startup code.
When one axis table supplies live settings and static defaults, derive both
from that table.

A transport may keep `HandleOutput` in `rpc.go` when replies must precede
conversation events. Keep event interpretation in `output.go` or `events.go`
according to the unit that owns dispatch. A protocol family may share startup
and base behavior, but each concrete provider keeps its own agent, start, and
registration files.

Give tests file names that match the roles they exercise. Do not add a
redundant `native_` prefix to a file name when the package has one transport.

### The three-layer chat render pipeline

A change that crosses a layer is almost always misplaced. Each layer's
`README.md` holds the long form.

1. **`components/chat/providers/` — extraction.** A plugin reads ONE agent's
   wire format, owns its tool names, method names and envelope tokens, and
   returns provider-neutral model. It draws nothing (no JSX, DOM or Solid
   signal). The one entry is `extractChatRow` (`rowExtraction.ts`), via
   `prepareChatRow`.
2. **`components/chat/model/` — the neutral model:** the `ChatRow` union,
   `ToolCall`, one file per `ToolKind` in `model/tools/`. It knows no provider
   and draws nothing: no `.tsx`, no presentation type — a model-owned union
   (`ReminderSeverity`, `ToolIconHint`) states intent and layer 3 maps it. It
   imports only `~/lib/*`, `~/generated/*`, `~/models/*`, its own tree and the
   three pure diff modules, **type imports included**.
3. **`components/chat/results/` — rendering** from the model and design tokens.
   `renderRowContent` (`rowRenderers.tsx`) is the one place a row kind becomes
   markup; its `switch` is exhaustive through `assertNever`, so a new kind is a
   compile error. Layer 3 never imports `../providers/`, parses provider bytes,
   or branches on `AgentProvider`, a tool name or a wire token.

Only three control surfaces in `providers/` draw: `codex/CodexControlActions.tsx`,
`cursor/CursorControlActions.tsx`, `pi/PiPlanApprovalActions.tsx`.

Enforcement, from `eslint/chatPipelinePlugin.ts`:

- `layer-imports` — every import form, both directions.
- `no-provider-decision` — a provider comparison, switch or keyed lookup, a
  helper that decides for you, or a bare wire token as a string literal. The
  wire tokens come from each contract table that states `frameKind` in
  `PROVIDER_PROTOCOLS` (`scripts/generate-contracts.mjs`), so a frame kind that
  a contract gains joins the lint with no edit to the rule.
- `no-forbidden-assertion`.
- `plugin-registration-only` — a `plugin.ts` holds the registration and imports
  each hook.

A `no-restricted-syntax` block in `eslint.config.ts` bans JSX under `providers/`
outside those three surfaces and test files.
`src/test-support/chatLayerStructure.test.ts` guards the structure the rules
assume; `src/test-support/restrictedSyntaxKeepsBaseRules.test.ts` runs the real
linter over probe files so a scoped config block cannot silently un-guard a
tree.

Extracting a preview, summary or plaintext is layer 1's job in the browser,
never the Go backend's (the `frontend-owns-message-extraction` principle):
layer 1 is where raw shapes are known.

### Tests

- Backend: `testify/assert`, `testify/require`. Frontend: `vitest`.
- **Titles.** A `describe` spells its symbol exactly, whatever the case —
  `describe('DirectoryTree')`, `describe('MESSAGE_UI_DEFAULTS')`,
  `describe('createStableContext')`, `describe('ChannelManager openChannel')` —
  which `test/prefer-lowercase-title` allows via `ignore: ['describe']`. A
  `describe` with no single symbol opens lowercase
  (`describe('parses empty input')`). An `it`/`test` title is a lowercase
  sentence continuing "it" (`it('returns null for an empty payload')`); since
  `--fix` lowercases only the first letter, never open one with a capitalized
  name (`dEFAULT_MONO_FONT_FAMILY`). Never flatten a name's capitals
  (`describe('mcptoolcalldisplayname')` for `mcpToolCallDisplayName`).
  `src/test-support/noMangledTestTitles.test.ts` fails both.
- **Co-locate:** `foo.ts` → `foo.test.ts` beside it, `tests/e2e/` included
  (`helpers/mail.ts` → `helpers/mail.test.ts`). No `tests/unit/` mirror
  (`src/test-support/noMirroredUnitTests.test.ts`). Shared helpers live in
  `src/test-support/` (`~/test-support/…`).
- **The extension picks the runner**, everywhere: `.spec.ts` is Playwright,
  `.test.ts` is vitest, so a `.test.ts` in `tests/e2e/helpers/` runs in
  `task test-frontend` and never in E2E. `vitest.config.ts` excludes
  `tests/e2e/**/*.spec.ts` by name; `playwright.config.ts` pins
  `testMatch: '**/*.spec.ts'`, because Playwright's default `testMatch` also
  takes `*.test.ts` and would run it in a browser worker without vitest's API.
  `src/test-support/testFileNaming.test.ts` enforces both halves. Widening the
  vitest exclude back to `tests/e2e/**` would leave a co-located test under
  **neither** runner.
- Test provider-specific logic in that provider's test file (e.g. Claude's
  `previewText` in `providers/claude/plugin.test.ts`).
- **Inject what ends a transient state; never size a window with a sleep.**
  Four CI flakes had that shape: a 5ms sleep in a 10ms backoff overran on loaded
  macOS; an `interval/2` sleep passed the tick as Windows rounds up to ~15.6ms;
  a multi-megabyte write meant to block returned through Windows loopback. Take
  a `quartz.Clock` (`quartz.NewReal()` in production) driven via
  `internal/util/testutil` (`NewQuartzMock`, `DeadlineContext`,
  `WaitForTimer`), or a gate the test releases. Keep an unasserted deadline
  generous (30s).
- **A Go test that runs `git init` pins `core.fsmonitor false`, `gc.auto 0` and
  `maintenance.auto false` in the repo's config**, not via `-c`: a global
  `core.fsmonitor=true` spawns a daemon that outlives the command and writes
  under `.git/`, racing `t.TempDir` cleanup
  (`TempDir RemoveAll cleanup: ... directory not empty`, blamed on the test).
  `testutil.NewGitRepo` does this; a new helper must too.
- **Postgres/MySQL store suites need `-tags integration` and Docker:**
  `cd backend && go test -tags integration -run 'TestPostgresStore|TestMySQLStore' ./internal/hub/store/postgres/... ./internal/hub/store/mysql/...`
  (MySQL takes about 85s). Without the tag they report "no test files", and
  `go test ./internal/hub/store/...` covers **sqlite alone**, missing the MySQL
  lock / REPEATABLE-READ and Postgres null-time divergences that the shared
  `store/storetest/` suite exists to catch.

### E2E tests

One `leapmux dev` process and one mock model server serve the whole run; one
browser context and tab serve every spec; no test talks to a real model. Hence
`workers: 1` and `fullyParallel: false`, and about half an hour per full run —
prefer `task test-e2e -- <files>`. **CI does not run E2E**: neither `task test`
nor `task test-no-docker` reaches `test-e2e`, so run it locally before claiming
an E2E change works.

- **Never start your own hub.** `startSuiteServer` (`helpers/suiteServer.ts`)
  owns it; the `leapmuxServer` fixture gives its URL, tokens and worker id. Take
  a fresh WORKSPACE. A spec that needs its own process (a restart, a
  deregistration, a second worker) spawns it with `hubSpawnEnv()` merged with
  `leapmuxServer.agentEnv`, so agents still reach the mock.
  `helpers/server.test.ts` fails a `hub`, `solo`, `dev` or `worker` spawn that
  skips that helper, since a raw `process.env` carries real provider
  credentials.
- **Undo page state in the reset, not the spec.** `resetSharedPage`
  (`fixtures.ts`) restores listeners, routes, cookies, permissions, storage,
  viewport, device metrics and every media-emulation key. A spec's own cleanup
  runs only on success, so one failure would poison every later spec.
  `203-shared-tab-isolation.spec.ts` guards this; `mediaEmulationReset` is typed
  `Required<…>`, so a new Playwright media feature fails to compile until
  handled.
- **Isolate explicitly** in `ISOLATED_CONTEXT_SPECS`. `isMobile`, `hasTouch` and
  a non-default `deviceScaleFactor` isolate automatically — they belong to a
  CONTEXT, not a page.
- **Script the model; never ask it.** The mock answers three model APIs and
  the services of Cursor, Kiro and Amp. The `modelScript` fixture states each
  turn:
  - `queue` — an ordered turn.
  - `rule` — a turn that cannot be placed in order, such as a subagent's.
  - `fallback` — however many turns a provider starts by itself.
  - `prompt` — marks a prompt as this test's.
  - `waitForSteps` — synchronizes.
  - `allowUnconsumed` — a turn interrupted on purpose.

  An unscripted content turn FAILS with its request recorded.
- **Tool calls come from `helpers/providerToolCalls.ts`**, the one home of each
  provider's tool vocabulary; `satisfies` makes a new provider a type error.
  Never hand-roll a wire shape.
- **Never seed through the database** — no message or control-request row. A
  direct write produces a shape no live provider reaches; script it instead.
- **Never `test.skip()` around model behaviour**; script the turn.
  `requireRegistryRow` fails rather than skips and takes no `test` parameter.
- **No per-call `{ timeout: … }` overrides** on `expect`, `locator.waitFor` or
  the rest. The one exception sets the bar: `193-tool-running-badge.spec.ts`
  waits on the Claude CLI's fixed 30-second `setInterval` through a named
  constant documenting that period. Discuss before adding another.
- **Scope a chat locator to `:visible`, and a sidebar locator to `:visible` plus
  `.first()`.** `ChatView` keeps a hidden premeasure copy of each unmeasured row
  (same test ids and text), so a page-rooted chat locator can match TWO elements
  and fail strict mode; use `helpers/ui.ts` (`assistantBubbles`, `userBubbles`,
  `messageBubbles`, `messageContents`, `visibleOnly`) on the OUTERMOST locator.
  The SIDEBAR is mounted twice for real (desktop and mobile): the second copy
  may be visible, never hydrates worker-side metadata, and can intercept a
  hover. Use `workspaceRow` / `treeRow` / `branchGroupRow` and
  `sidebarLeafLabels` / `sidebarLeafIds`, never a raw `querySelector`. The agent
  info card renders on two surfaces; scope each row to its popover.
  `src/test-support/visibleChatLocators.test.ts` rejects an unscoped test-id or
  `text=` locator.
- **Wait on the WORKER, not the tab count or the hub's list** — both are
  optimistic CRDT state. `emitRemoveTab` tombstones at once while `CloseAgent`
  is still stopping the process, and `listAgentsViaAPI` reads the hub's
  `ListTabs`, already cleared by that tombstone. Poll `inspectLastTabCloseViaAPI`
  for a close verdict, or `waitForAgentStartupViaAPI` for startup (it waits for
  not-STARTING, so it does NOT distinguish a failed agent). The worker judges
  "last tab on the branch?" from its own rows, so closing two tabs in a row
  races the first teardown.

### Frontend CSS (vanilla-extract)

Prefer `var(--space-N)` tokens to pixel literals for `gap`, `margin*` and
`padding*` (`@knadh/oat`: `--space-1` = `0.25rem` (4px), `--space-2` = `0.5rem`
(8px), `--space-3` = `0.75rem` (12px), …) — but not for `borderRadius`, fixed
`width`/`height` (resizers, scrollbars) or absolute positioning offsets, which
are magic numbers unrelated to the scale.

### Imports

Prefer direct imports to re-export aliases. Never add
`export { foo as bar } from '...'` in a barrel/style file only for a
context-specific name; import the canonical name, and rename the canonical
export if it is too generic. Leave an existing alias unless you touch its file
anyway.

### Tooltips

Hover text on an interactive element uses `<Tooltip>`
(`~/components/common/Tooltip`), never a bare `title`. Pass `ariaLabel` when the
control has no visible text, so the tooltip is its accessible name too.

```tsx
<Tooltip text="Remove link, keeping the text" ariaLabel>
  <button onClick={remove}><Icon icon={Trash2} size="xs" /></button>
</Tooltip>
```

**`title` on a DOM element is a lint error** (`no-restricted-syntax` in
`eslint.config.ts`) with no exception, disabled controls included; the lint
message states why, and `Tooltip.tsx` documents how it covers a disabled
control. The rule matches **lowercase** elements only, because `title` on a
component is its own prop (`<Dialog title>` is a heading, `<IconButton title>` a
tooltip). So a component that spreads props onto a DOM node must omit `title`
from its prop type, as `IconButton` and `ConfirmButton` do — the linter cannot
see through the spread.

### Dropdowns and one-of-N choices

Never render a native `<select>`:

- `<PillGroup>` (`~/components/common/PillGroup`) for up to four options on one
  row; it supplies `role="radiogroup"`, roving tabindex and the arrow-key
  contract.
- `<DropdownMenu>` + `<DropdownMenuCheckableItem kind="radio">`
  (`~/components/common/DropdownMenu`) for anything longer, dynamic or with no
  upper limit, as `AgentProviderSelector` and `PreferencesNav` do. A list with
  no upper limit gets a filter box: render the menu `as="div"` so a click inside
  does not dismiss it, and close from the item's own handler.

A `<select>` opens the unthemed OS picker, renders text only (no colour swatch,
icon or second line, which `ThemeChooser` needs), and keeps its selected index as
browser state that callers must repair by hand after a refused write or an
option-list swap. A menu derives from props and cannot drift.

### Browser storage

Never call `localStorage`, `sessionStorage` or `indexedDB` directly: route every
read, write and delete through `~/lib/browserStorage`, and open every database
through `~/lib/idb` (`createIdbConnection`). Callers pass a LOGICAL name
(`'key-pins'`, `'worker-info:w-1'`); the module composes the stored key. A test
resets a store with `localStorageClearForTests` / `sessionStorageClearForTests`
/ `resetBrowserStorageForTests`, not `clear()`. A module that MIRRORS an
account-scoped key in memory subscribes to `onStorageAccountChange`, so the
mirror follows the account.

`browserStorage.ts` and `browserStorageDb.ts` document the rest: the IndexedDB
and sessionStorage backends, expiry and `runCleanup`, the write-behind queue,
and account hydration.

To add a key:

1. Add the constant (`KEY_*`) or prefix (`PREFIX_*`) to `browserStorage.ts`.
2. Register it in `LOCAL_KEY_SPECS` (IndexedDB) or `SESSION_KEY_SPECS`
   (sessionStorage), and choose for a local key:
   - `access`: `sync` only for a reader that cannot await (a `createSignal`
     initializer, a `createMemo`, a constructor); `async` for everything else,
     and for any family with no upper limit.
   - `scope`: `'account'` for anything a user owns; `'device'` only for state
     every account on the origin shares.

   `satisfies` makes a missing `scope` or `access` a compile error.
3. Use its tier's helpers. The wrong tier is a compile error (`SyncLocalKey` /
   `AsyncLocalKey`), and an unregistered name throws, so a mistake fails
   visibly instead of vanishing at the next sweep.

Guards: `no-restricted-globals` / `no-restricted-properties` in
`eslint.config.ts` reject the storage globals outside the gateway (and any
`dexie` import outside `~/lib/idb`); `src/test-support/storageKeysAreRegistered.test.ts`
fails for an exported key constant that neither table registers, or a name both
register.

## Git

Never commit generated files. `generated/` output (sqlc, proto stubs, etc.) is
gitignored; exclude it when staging.
