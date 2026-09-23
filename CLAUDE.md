# LeapMux

Multi-agent coding assistant platform supporting Claude Code and Codex.

- Backend: Go
- Frontend: SolidJS with vanilla-extract CSS (`.css.ts` files)
- E2E: Playwright
- Desktop: Tauri (Rust + Go sidecar)

## Project constraints

LeapMux is **pre-release**. Backward compatibility is not a concern, and a
review finding about one is noise.

- Edit the `00001_initial.sql` migration in all three dialects in place. Never
  add a migration file.
- Renumber, regroup, replace or remove a protobuf field freely. The codebase
  still `reserved`s a freed number by its own choice; that is a preference, not
  a requirement.
- Change a public interface when a better design wants it. No installation is
  deployed, so a compatibility shim is dead weight.

Do not raise an upgrade path, wire compatibility or migration hygiene as a
concern, and do not propose a shim for one.

## Build system

Use `task` (`Taskfile.yaml`) targets, not the underlying tools directly.

**Run one `task` pipeline at a time.** Every pipeline runs `prepare-backend`,
which deletes the embedded frontend tree under
`backend/internal/hub/generated/frontend/public` and copies
`frontend/.output/public` into it. That pair is not atomic, so a second
pipeline's test binary reads a half-copied tree. Nothing in the failure points
at the copy: `hub` and `internal/hub/frontend` derive the Content Security
Policy from the inline `<script>` tags of the EMBEDDED `index.html`, so a
partial copy surfaces as about seventeen failures in `TestPolicy`,
`TestResolveFrontend` and their siblings, each reading `does not contain
"'sha256-"`. Chain the suites in one command
(`task test-backend; task test-frontend; task lint`) instead of starting them as
concurrent jobs. A starved run also looks exactly like a hang, so a stacked run
sends you chasing a deadlock that is not there.

- Frontend package manager: `bun` (lock: `bun.lock`)
- Proto generation: `buf generate` (via `task generate-proto`)
- Contracts generation: `scripts/generate-contracts.mjs` (via `task generate-contracts`)
- SQL generation: `sqlc generate` (via `task generate-sqlc`)

### Contracts: single source of truth for cross-language values

Any constant, string, limit, or table consumed on BOTH sides of a language
boundary (Go hub/CLI/worker and the TS browser client) lives in
`contracts/<name>.json` — never hand-written twice. `task generate-contracts`
validates each contract against its sibling `<name>.schema.json`, runs the
semantic cross-checks (derived arithmetic, enum coverage via `buf build`
descriptors, graph acyclicity), and emits Go into `backend/generated/contracts/`
and TS into `frontend/src/generated/contracts/` (both gitignored; CI generates
before building).

- `wire.json` — channelwire limits, timing, close reasons, the WS route /
  query-param / subprotocol vocabulary, the Noise nonce limits (soft rekey
  trigger and hard wrap bound), and the frame length prefix. `headers.json` —
  cross-program HTTP headers (both elevation headers,
  credential-rejected). `retry.json` — the events-rejection retry policy.
  `chat-history.json` — the message page limit and browser catch-up gap limit.
  `user-settings.json` — the account-setting vocabulary: the proto key of every
  setting, plus the default, the enum tokens and the numeric limits of the ones
  whose value is a closed set or a range. The hub validates against these
  (`usersettings/keys.go`) and the browser parses against them
  (`PreferencesContext.tsx`), so a bound that differed stored a value the other
  side then discarded for its fallback. The three Desktop enums state only
  their default here: a THIRD language spells their tokens, so those stay in
  `desktop.json`, and the generator cross-checks the two.
  `worker-vocab.json` — notification-type tokens, the notification-thread
  discriminator, the Codex rate-limit token, and the model sentinels.
  `goose-protocol.json` — Goose permission modes. `copilot-protocol.json` —
  Copilot's native event, tool, mode and permission vocabulary, plus the
  session-mode option-group id and the approval-scope words its control
  surface sends.
  `providers.json` — AgentProvider display names / CLI aliases / parse
  aliases (agentlabels and agentProviderLabel consume the generated tables).
  `scopes.json` — the scope vocabulary: wire tokens, Preferences
  descriptions, consent-screen sentences, categories, implied-by graph.
  `theme-default.json` — the default palette + the OAuth pages' subset.
  `validate.json` — validation policy parameters (byte limits, strip/fold/
  refused character classes, reserved usernames). `desktop.json` — the Tauri
  event names (Rust shell emits, webview listens — all seven of them,
  including the sidecar-log and menu events), the env vars Rust passes
  the Go sidecar, and `windowBehavior`, the enum tokens of the five Desktop
  account settings (the one setting family a THIRD language spells: the Rust
  shell matches them out of the `set_desktop_behavior` payload). It also emits
  a RUST module (`desktop/rust/src/generated/contracts.rs`, include!d from
  main.rs), so `prepare-desktop` depends on `generate-contracts`.
- Go consumers import `generated/contracts` directly: one Go spelling per
  contract constant (`contracts.MaxMessageSize`, `contracts.WSRouteChannel`).
  `channelwire` keeps only the Go-owned limits no contract holds
  (`WSReadLimit`, `UserEventsReadLimit`) and the wire helpers; it does not
  alias generated constants. The frontend likewise imports
  `~/generated/contracts/*` directly (no re-export shims).
- To change a value: edit `contracts/<name>.json`, run `task generate`, use.
  Adding a proto enum value fails `generate-contracts` until its contract
  entry exists — metadata cannot be forgotten.
- What does NOT go in contracts: values consumed by one side only (Go-only
  limits stay in `wire.go`; FE-only retry policies stay in the frontend), and
  dual-implemented ALGORITHMS — those stay differential: the
  `testdata/*_conformance.json` corpora (and `noise_rekey_vectors.json`)
  remain the executable spec both suites replay.
- This is a rule about CONTRACTS, not about proto. A single-side enum still
  takes its numbering from a proto enum — see "Enum columns store proto enum
  ordinals" below. Crossing a language boundary is what adds a contract entry
  on top; it is not what earns an enum its numbering.

### JSON Schema validation (no schemaless JSON)

Every project-written JSON file must validate against a JSON Schema: run
`task validate-json` (also part of `task lint`, `task test`, and
`task test-no-docker`, so every CI OS job enforces it). Scope and rules live
in `scripts/validate-json.mjs`: `contracts/`, `testdata/` (root and package
fixtures) resolve a SIBLING `<name>.schema.json`; the vendored syntax themes
and license-override metadata use shared schemas stated on their rule. An
in-scope file with no schema is a hard failure — a new fixture cannot appear
without a schema stating its shape. Tool-owned JSON (package.json, tsconfig,
tauri configs, lockfiles) is out of scope on purpose.

### vanilla-extract `.css.ts` files

Never write a bare `*.css.ts` basename inside a `.css.ts` file — not in code, and not in a comment. Write `~/styles/global.css.ts` or `./widgets/SpanLines.css.ts`, never `global.css.ts`. A bare basename makes the vanilla-extract compiler fail the whole module with `Styles were unable to be assigned to a file`, pointing at an unrelated line in that file. Every test that imports the module then fails to load, which reads as a broken component rather than a broken comment.

### sqlc files

`backend/internal/hub/store/{sqlite,postgres,mysql}/db/queries/*.sql` and any other sqlc query files MUST contain only ASCII characters. The sqlc parser fails on non-ASCII bytes (typically inside comments) with a misleading `mismatched input 'SELECr'`-style error that points at the wrong line. Use `--` (double hyphen) and plain ASCII punctuation instead of `—` (em-dash) or smart quotes.

## Common commands

- `task generate` — proto + sqlc generation
- `task build` — full build (backend + frontend)
- `task lint` — all linters
- `task test` — all tests
- `task test-e2e -- <files>` — run only the affected E2E specs. The full suite is one worker and about half an hour; see **E2E tests** below.
- `task lint-backend` / `task lint-frontend` / `task lint-desktop`
- `task test-backend` / `task test-frontend`

Lint Rust/desktop code with `task lint-desktop`, not `cargo clippy` directly. The task builds the Go sidecar binary first, which Tauri's bundle resources point at `../go/bin/*`. Running `cargo clippy` directly fails with a misleading build error.

## Coding conventions

### Enum columns store proto enum ordinals

A database column whose values are a closed set is an enum, and every enum in
this project takes its numbering from a proto enum. The column stores the
ordinal as an integer — never the value's name.

This holds whether or not the value crosses a language boundary. A hub-internal
vocabulary the browser never sees still gets a proto enum; the ones that had
none live in `proto/leapmux/v1/hub_storage.proto`, which exists for exactly
that and carries no message or RPC.

- **Give the column a CHECK that states the range**, and start it at 1. Proto3
  fixes UNSPECIFIED at 0, an unset Go field holds 0, and no column here has a
  state 0 could mean — so a write that forgot the value fails instead of
  recording one nobody chose. `agents.goal_status` is the one exception, where
  0 is the real state "no goal", and it says so at the column.
- **Admit less than the enum declares when a value is derived or belongs to
  another table.** `AGENT_GOAL_STATUS_DORMANT` and the two
  `ControlResponseState` values that describe a request with no answer row are
  both outside their column's CHECK, each with the reason at the column.
- **Bind the ordinal as a query parameter**, from the Go constant, so a
  renumber propagates. Spell a literal only where a parameter cannot reach: a
  migration, or a partial-index predicate (SQLite matches those syntactically,
  so a bound `?` makes the index ineligible). Every such literal needs a test
  that pins it — `enum_column_numbering_test.go` in `hub/store` and `worker/db`
  are those tests, and a schema comment identifies the one that guards it.
- **The Go domain type is a DEFINED type over the proto enum**
  (`type Status leapmuxv1.BackgroundTaskStatus`), not an independent iota and
  not an alias. The ordinals are then one numbering, the conversion each way is
  a cast, and the type still carries its own methods.
- **A payload vocabulary is a separate decision from storage.** Some of these
  values also travel as WORDS inside a notification payload or an RPC field
  (`bgtask.StatusWire`, `agent.GoalStatusWire`, `oauth.ProviderTypeWire`,
  `store.AppRegistrationSourceWire`). Those functions stay, and each says at its
  definition that it is not the storage format. Do not add the inverse unless a
  caller reads that vocabulary inward.

Why: the alternative spells one vocabulary in Go and again in three dialects of
SQL, with nothing to keep them in step. Renaming a Go constant used to leave a
`status IN ('completed','failed',…)` list stale, and a `FromWire` that fell
through to a default turned the drift into a plausible wrong value rather than
an error.

### Provider-specific logic belongs in the provider, not shared code

LeapMux supports ten agent providers. Five read their own native protocol — Claude
Code, Codex, Copilot, Pi and ZCode. Five speak the Agent Client Protocol — OpenCode,
Cursor, Kilo, Goose and Reasonix — and reach the worker through `acpStart`. Copilot
is NOT one of them: `copilot_connection_test.go` and `copilot_native_session_test.go`
both assert its arguments hold no `--acp`. Anything that depends
on a **single provider's wire format or message shapes** MUST live in that provider's
plugin/implementation — never hardcoded into shared code (a package-level helper, a
shared `default*` function, or a `switch` on provider). Shared code stays
provider-neutral and delegates the provider-specific decision.

- **Backend (Go):** the `Provider` interface in
  `backend/internal/worker/agent/provider.go` is the home for per-provider decisions.
  Add a method there (e.g. `IsSelfDisplayingControlTool`) and dispatch via
  `agent.ProviderFor(provider)`. Do NOT put a provider's tool names / method names /
  envelope shapes in a package-level function that shared service code calls.
- **Frontend (TS):** the `Provider` plugin interface in
  `frontend/src/components/chat/providers/registry.ts` is the home. Add a method
  (e.g. `previewText`, sibling of `extractQuotableText`) and implement it per plugin;
  a genuinely provider-neutral shape (`{content}`, `{controlResponse}`) can share a
  `default*` helper that plugins delegate to, but the Anthropic/Codex/Pi/ACP-specific
  parsing stays in that plugin. The renderer layer is where each provider's raw
  message shapes are known — see the `frontend-owns-message-extraction` principle.

Why: hardcoding one provider's shape into shared code silently breaks or half-serves
every other provider and is a second source of truth that drifts. If you write a
provider's tool/method name outside its plugin, move it into the plugin behind
an interface method.

### The three-layer chat render pipeline

The frontend transcript is three layers, and a change that crosses one is
almost always in the wrong place. Each layer carries its own `README.md`, which
is the long form of what follows.

1. **`components/chat/providers/` — extraction.** A plugin reads ONE agent's
   wire format and returns provider-neutral model. It owns every tool name,
   method name and envelope token that agent uses. It draws nothing: no JSX, no
   DOM, no Solid signal. `extractChatRow` (`rowExtraction.ts`), reached through
   `prepareChatRow`, is the one entry.
2. **`components/chat/model/` — the provider-neutral model.** The `ChatRow`
   union, `ToolCall`, and one file per `ToolKind` under `model/tools/`. Nothing
   here knows a provider and nothing here draws. It imports `~/lib/*`,
   `~/generated/*`, `~/models/*`, its own tree, and the three pure diff modules
   — nothing else, **including type imports**. It holds no `.tsx` file and no
   presentation type: express the intent as a model-owned union
   (`ReminderSeverity`, `ToolIconHint`) and let layer 3 map it to a component.
3. **`components/chat/results/` — the renderers.** They read the model and the
   design tokens. `renderRowContent` (`rowRenderers.tsx`) is the one place a row
   kind becomes markup, and its `switch` is exhaustive through `assertNever`, so
   a new kind is a compile error rather than a row that draws nothing. Layer 3
   never imports `../providers/`, never parses a provider's bytes, and never
   branches on `AgentProvider`, a tool name or a wire token.

Four control surfaces in `providers/` draw on purpose and are the only
exceptions: `codex/CodexControlActions.tsx`, `cursor/CursorControlActions.tsx`,
`pi/PiControlActions.tsx`, `pi/PiPlanApprovalActions.tsx`.

The boundary is enforced, not merely documented. `eslint/chatPipelinePlugin.ts`
supplies four rules — `layer-imports` (every import form, both directions),
`no-provider-decision` (a provider comparison, a switch, a keyed lookup, a
helper that decides for you, or a bare wire token as a string literal),
`no-forbidden-assertion`, and `plugin-registration-only` (a `plugin.ts` holds
the registration and imports each hook). `src/test-support/chatLayerStructure.test.ts`
guards the structure the rules assume, and
`src/test-support/restrictedSyntaxKeepsBaseRules.test.ts` runs the real linter
over probe files so a scoped config block cannot quietly un-guard a tree.

Why: the alternative puts one agent's shapes behind a module that exists to
draw, where it half-serves the other nine. See also the
`frontend-owns-message-extraction` principle — extracting a preview, a summary
or plaintext from a message is layer 1's job in the browser, never the Go
backend's, because the renderer layer is the one that knows the raw shapes.

### Tests

- Backend: `testify/assert`, `testify/require`.
- Frontend: `vitest`. A `describe` identifies the symbol under test and **spells that symbol exactly**, whatever its case: `describe('DirectoryTree')`, `describe('MESSAGE_UI_DEFAULTS')`, `describe('createStableContext')`, `describe('ChannelManager openChannel')`. `test/prefer-lowercase-title` is configured with `ignore: ['describe']` for exactly this, so a capital is legal there and needs no workaround. A describe that identifies no single symbol still opens lowercase (`describe('parses empty input')`) — never Title Case prose.
- An `it` or `test` title is a **sentence** that continues the word "it", so it starts lowercase: `it('returns null for an empty payload')`. The lint rule still enforces that half, and its `--fix` lowercases the first letter alone — so a case title must never open with a name that keeps its capital, or `--fix` misspells it (`dEFAULT_MONO_FONT_FAMILY`). Put the name later in the sentence instead.
- **Never flatten a name's capitals** in any title: `describe('mcptoolcalldisplayname')` for `mcpToolCallDisplayName` spells an identifier nobody can search for. `src/test-support/noMangledTestTitles.test.ts` fails the suite on both faults — the autofix mangle, and a title that drops the capitals of a name its own file knows.
- **Unit tests are co-located** with the code they test: `foo.ts` → `foo.test.ts` in the same directory. This holds under `tests/e2e/` too — an E2E helper carries its own `.test.ts` beside it (`helpers/mail.ts` → `helpers/mail.test.ts`). Do **not** add a second test file for a module under `tests/unit/` — that mirror no longer exists (see `src/test-support/noMirroredUnitTests.test.ts`, which fails the suite if it comes back). Shared unit-test helpers live in `src/test-support/` (imported via `~/test-support/…`).
- **The file extension picks the runner**, everywhere: `.spec.ts` is Playwright, `.test.ts` is vitest. So a `.test.ts` under `tests/e2e/helpers/` runs in `task test-frontend` — no browser, no hub, milliseconds — and never in the E2E suite. Both configs are pinned to this (`vitest.config.ts` excludes `tests/e2e/**/*.spec.ts` by name, not `tests/e2e/**`; `playwright.config.ts` sets `testMatch: '**/*.spec.ts'`), and `src/test-support/testFileNaming.test.ts` fails the suite when a file is on the wrong side or a config stops enforcing its half. Do not widen the vitest exclude back to `tests/e2e/**`: with Playwright pinned to `.spec.ts`, a co-located test would then run under **neither** runner. Playwright's own default `testMatch` takes `*.test.ts` as well, which is why the pin is there — without it those tests run in a browser worker, where vitest's API does not exist.
- Unused imports cause lint failures (strict).
- Test provider-specific logic in that provider's test file (e.g. Claude's `previewText` in `providers/claude/plugin.test.ts`), not in a shared module's test.
- **Inject the thing that ends a transient state; never size a window with a sleep.** Four CI flakes here had one shape: the test asserted something true only inside a window the environment sized. A 5ms sleep inside a 10ms backoff overran on a loaded macOS runner; a sleep of `interval/2` passed the tick because Windows rounds up to ~15.6ms; a multi-megabyte write meant to stay blocked returned at once because Windows loopback absorbed it. Take a `quartz.Clock` in production code (`quartz.NewReal()` by default) and drive a mock through `internal/util/testutil` (`NewQuartzMock`, `DeadlineContext`, `WaitForTimer`), or inject a gate the test releases. A deadline no test asserts should be generous (30s), never tuned down to keep a test fast.
- **A Go test that shells out to `git init` must pin `core.fsmonitor false`, `gc.auto 0` and `maintenance.auto false` in the repo's own config** — not as `-c` flags on the creating command. A host with `core.fsmonitor=true` set globally spawns a daemon that outlives the command and keeps writing under `.git/`, which races `t.TempDir` cleanup and fails with `TempDir RemoveAll cleanup: ... directory not empty`. It names the test that owned the directory, not the daemon, so it reads as a flaky test. `testutil.NewGitRepo` does this; a new helper must too.
- **A cross-tab storage assertion must scope itself.** `~/lib/browserStorageDb` publishes on a module-level `BroadcastChannel`. Vitest isolates the module registry per file but not the PROCESS, so two files share one bus and each receives the other's writes with a differing `from`, which suppresses nothing. Filter on `kvInstanceIdForTests()` for what this tab published, filter a heard set to the keys the case names, and give a file's fixture keys a segment unique to that file.
- **Do not propose happy-dom.** It builds a DOM about 2.7x faster than jsdom (measured: 60.6s → 33.6s on the full suite), and it returns `''` from `getComputedStyle(el).overflowX` where both a browser and jsdom return `'visible'`. `Tooltip.tsx`'s clip detection reads exactly that, so under happy-dom every element looks clipped and the "not clipped" branch stops being reachable from a test. Solve that first or leave it alone.
- **The Postgres and MySQL store suites need a build tag and Docker**: `cd backend && go test -tags integration -run 'TestPostgresStore|TestMySQLStore' ./internal/hub/store/postgres/... ./internal/hub/store/mysql/...`. Without the tag they report "no test files", and a plain `go test ./internal/hub/store/...` exercises **sqlite alone**. The three dialects share one behavioural suite under `store/storetest/`, so a change to cross-dialect logic that runs only on sqlite misses a MySQL lock or REPEATABLE-READ divergence and a Postgres null-time one. The MySQL run takes about 85s.

### E2E tests

The suite builds its world ONCE. One `leapmux dev` process and one mock model server serve the whole run, one browser context and one tab serve every spec, and no test talks to a real model. `workers: 1` and `fullyParallel: false` follow from the shared tab, so a full run is sequential and takes about half an hour — prefer `task test-e2e -- <files>`.

**CI does not run this suite.** Neither `task test` nor `task test-no-docker` reaches `test-e2e`, and those are what the workflow calls. A green CI run says nothing about the E2E suite, so run it locally before you claim an E2E change works.

- **Never start your own hub.** `startSuiteServer` (`helpers/suiteServer.ts`) owns the one process, and the `leapmuxServer` fixture hands you its URL, tokens and worker id. A test takes a fresh WORKSPACE, not a fresh process. The few specs that genuinely need their own — a restart, a deregistration, a second worker — spawn it with `hubSpawnEnv()` merged with `leapmuxServer.agentEnv`, so their agents still reach the mock endpoint. `helpers/server.test.ts` fails the suite for a `hub`, `solo`, `dev` or `worker` spawn that skips that helper, because a raw `process.env` carries the developer's real provider credentials into the test.
- **Undo page state in the reset, not in the spec.** `resetSharedPage` (`fixtures.ts`) returns the one tab to a known state between tests: listeners, routes, cookies, permissions, storage, viewport, device metrics and every media-emulation key. Anything a spec changes on the page or the context belongs THERE. A cleanup written in the spec runs only when that spec passes, so a spec that fails poisons every later one. `203-shared-tab-isolation.spec.ts` guards this, and `mediaEmulationReset` is typed `Required<…>` so a media feature Playwright adds later is a compile error until the reset names it.
- **A spec that cannot share the context says so.** `ISOLATED_CONTEXT_SPECS` names them. `isMobile`, `hasTouch` and a non-default `deviceScaleFactor` force isolation on their own, because all three belong to a CONTEXT rather than to a page.
- **Script the model; never ask it.** Every provider reaches the mock model server, which answers three model protocols plus Cursor's own Connect stream. A spec states the turn through the `modelScript` fixture — `queue` for an ordered turn, `rule` for one it cannot place positionally (a subagent's own turns), `fallback` for however many turns a provider runs by itself, `prompt` to mark a prompt as this test's, `waitForSteps` to synchronize, `allowUnconsumed` for a turn the test interrupts on purpose. An unscripted content turn FAILS with its request recorded, so a missing script names itself instead of answering something plausible.
- **A provider's tool call comes from `helpers/providerToolCalls.ts`.** That is the one place each provider's tool vocabulary appears — the same rule as "Provider-specific logic belongs in the provider" above. Never hand-roll a wire shape in a spec. `satisfies` there makes a new provider a typecheck failure rather than a test that scripts a tool no agent offers.
- **Never seed through the database.** No spec writes a message row or a control-request row. Script the transcript through the mock endpoint instead: a direct write produces a persisted shape no live provider reaches, so the test passes against something the product cannot produce.
- **Never `test.skip()` around model behaviour.** A model that "might not" call a tool is a scripted turn now, so the branch that skipped is unreachable and the assertion runs every time. `requireRegistryRow` fails rather than skips, and it takes no `test` parameter to skip with.
- **Do NOT pass per-call `{ timeout: … }` overrides** to `expect`, `locator.waitFor`, and the rest. The project timeout already applies and an override is noise. There is ONE in the suite, and its shape is the bar: `193-tool-running-badge.spec.ts` waits on a 30-second `setInterval` inside the Claude CLI that no environment variable moves, so its deadline is a named constant whose doc block states the period it answers to. Discuss before adding a second.
- **Scope a chat locator to `:visible`, and a sidebar locator to `:visible` plus `.first()`.** `ChatView` mounts a faithful copy of every row whose height is unknown inside its hidden premeasure root — same test ids, same text — so a page-rooted chat locator transiently resolves to TWO identical elements and dies on strict mode. Use the helpers in `helpers/ui.ts` (`assistantBubbles`, `userBubbles`, `messageBubbles`, `messageContents`, `visibleOnly`); only the OUTERMOST locator needs it. The SIDEBAR is worse, because it is mounted twice for real (a desktop element and a mobile one): the second copy is often visible too, never hydrates its worker-side metadata, and can intercept a hover — so go through `workspaceRow` / `treeRow` / `branchGroupRow` and read the DOM with `sidebarLeafLabels` / `sidebarLeafIds`, never a raw `querySelector`. The agent info card is mounted twice again, on two surfaces, so scope each row to the popover under test. `src/test-support/visibleChatLocators.test.ts` fails the suite for a hand-written unscoped one.
- **Wait on the WORKER, not on the tab count or the hub's list.** Both are optimistic CRDT state that settles first: `emitRemoveTab` applies the tombstone at once, so the tab leaves the bar while `CloseAgent` is still stopping the process, and `listAgentsViaAPI` reads the hub's `ListTabs`, which the same tombstone already cleared. Poll a worker-backed RPC instead — `inspectLastTabCloseViaAPI` for a close verdict, `waitForAgentStartupViaAPI` for startup, which waits for not-STARTING and so does NOT distinguish a failed agent. The worker decides "is this the last tab on the branch?" from its own rows, so a test that closes two tabs in a row races the first teardown.
- **Nothing detects the mock drifting from a real provider's wire format.** No test talks to a real model, so every provider's tests stay green while its CLI changes underneath them. That gap belongs with the `testdata/*_conformance.json` corpora, which both suites already replay — not with a Playwright project selected by a tag. A `@real-provider` tag and its project were deleted for matching no test at all.

### Frontend CSS (vanilla-extract)

Prefer `var(--space-N)` design tokens over equivalent pixel literals for `gap`, `margin*`, and `padding*`. The token scale (from `@knadh/oat`):

- `--space-1` = `0.25rem` (4px)
- `--space-2` = `0.5rem` (8px)
- `--space-3` = `0.75rem` (12px)
- …

Does NOT apply to non-spacing px values: `borderRadius`, fixed `width`/`height` (resizers, scrollbars), absolute positioning offsets. Those are magic numbers, unrelated to the spacing scale.

### Imports

Prefer direct imports over re-export aliases. Do NOT add `export { foo as bar } from '...'` in a sibling barrel/style file just to give a symbol a context-specific name — import the canonical name directly at every call site. If the canonical name is too generic, rename the canonical export instead. Existing re-export aliases: leave them unless touching that file for another reason.

### Tooltips

Use the `<Tooltip>` component (`~/components/common/Tooltip`) for hover text on an interactive element. Do NOT use a bare `title` attribute — it renders the OS tooltip, which ignores the app's theme and typography, appears after a browser-controlled delay, and is invisible on touch.

```tsx
<Tooltip text="Remove link, keeping the text" ariaLabel>
  <button onClick={remove}><Icon icon={Trash2} size="xs" /></button>
</Tooltip>
```

Pass `ariaLabel` when the control has no visible text, so the tooltip also serves as its accessible name.

**`title` on a DOM element is a lint error** (`no-restricted-syntax` in `eslint.config.ts`). There is no exception, including a **disabled** control: `<Tooltip>` covers that case. It gives its wrapper a real box and listens there — a disabled element dispatches no pointer event of its own — and it leaves an offscreen description in `aria-describedby` for as long as the control is disabled, which is the only route to a screen-reader user there (a disabled element takes no focus, so the tooltip can never open from the keyboard).

Two things go wrong with a native `title`, and the second is silent. It renders the OS tooltip, which ignores the app's theme and typography, waits a browser-controlled delay, and never appears on touch. And on a control with no `aria-label`, a `title` long enough to state a reason **becomes the accessible name** — a screen reader then announces three sentences of remedy where "Add passkey" belongs, and every `getByRole(..., { name })` lookup stops matching.

The lint rule matches a **lowercase** element name only, because `title` on a component is that component's own prop: `<Dialog title>` is a heading, `<IconButton title>` is a tooltip. A component that spreads its props onto a DOM node closes that hole in the type system instead, by omitting `title` from its prop type — `IconButton` and `ConfirmButton` both do, and a new one that spreads DOM props must.

### Dropdowns and one-of-N choices

Never render a native `<select>`. Use:

- `<PillGroup>` (`~/components/common/PillGroup`) for a short fixed set — up to
  four options that fit on one row. It supplies `role="radiogroup"`, roving
  tabindex and the arrow-key contract.
- `<DropdownMenu>` + `<DropdownMenuCheckableItem kind="radio">`
  (`~/components/common/DropdownMenu`) for anything longer, dynamic or with
  no upper limit. Follow `AgentProviderSelector` and `PreferencesNav`, which
  already do this.

Why: a native `<select>` opens the OS picker, which ignores the app's theme and
typography — the same reason a bare `title` is banned for tooltips. It renders
text and nothing else, so a colour swatch, an icon or a second line is
impossible; `ThemeChooser` needs exactly that. And its selected index is browser
state, so every caller inevitably repairs the DOM by hand after a refused write
or an option-list swap — two such repairs were deleted when this project
removed the last selects. A menu derives from props and cannot drift.

For a list with no upper limit, give the menu a filter box: render it
`as="div"` so a click inside does not dismiss it, and close from the item's own
handler.

### Browser storage

Never call `localStorage`, `sessionStorage` or `indexedDB` directly. Route every read, write, and delete through `~/lib/browserStorage`, and open every database through `~/lib/idb` (`createIdbConnection`). A test resets a store with `localStorageClearForTests` / `sessionStorageClearForTests` / `resetBrowserStorageForTests`, not with `clear()`.

Callers pass a LOGICAL name (`'key-pins'`, `'worker-info:w-1'`). The module owns the physical layout and composes the whole stored key, so no call site builds one by hand.

TWO BACKENDS. The `leapmux:` family lives in **IndexedDB** (`~/lib/browserStorageDb`), because several of its families are unbounded and localStorage is synchronous main-thread I/O under a ~5 MB cap. `sessionStorage` stays on the Web Storage API, because its per-tab lifetime is load-bearing for the CRDT client identity and the tab pointers.

IndexedDB is asynchronous, so every localStorage-family key declares an `access` tier, and that picks the accessor:

- `sync` — MIRRORED in memory, so `localStorageGet` / `localStorageSet` / `localStorageRemove` stay synchronous. For a reader that cannot await: a `createSignal` initializer, a `createMemo`, the `onStorageAccountChange` callback, a constructor.
- `async` — not mirrored: `localStorageLoad` / `localStorageStore` / `localStorageDrop`, which return promises. The answer for everything else, and for every unbounded family.

Using the wrong accessor is a compile error (`SyncLocalKey` / `AsyncLocalKey`) and also throws at runtime.

`sessionStorage` keeps `sessionStorageGet` / `sessionStorageSet` / `sessionStorageHas` / `sessionStorageRemove`, and carries no `access`.

Why: every key is scoped to one account, and every row carries an expiration. Reads refresh it once it is within three hours of the full TTL, so a key stays alive as long as the app is touched within that TTL. `runCleanup` deletes any expired row, any row no registration matches, and any `leapmux:`-family key left in localStorage by a build that predates the move. It KEEPS another account's fresh key, which is the point of the scope.

Writes go through a coalescing write-behind queue, so a write is not durable the instant it returns. A caller that must know reads `StorageWrite.durable`; `persistedSeq` is the one that does. `App` flushes on `pagehide`.

Two registries hold every key, by logical name:

- `LOCAL_KEY_SPECS` — the IndexedDB-backed durable half.
- `SESSION_KEY_SPECS` — sessionStorage.

A local entry states `match` (`exact` or `prefix`), `scope`, `ttlMs` and `access`; a session entry omits `access`. A `scope: 'account'` key is stored at `leapmux:u:<userId>:<name>`, and that is the answer for anything a user owns. A `scope: 'device'` key is stored at `leapmux:<name>`, for state that guards a resource shared by every account on the origin; the two relay sequence marks are the only entries today, and they are also the only `monotonic` ones (a high-water merge, which the types allow on a `sync` key alone).

`hydrateStorageAccount(userId)` loads the synchronous tier and MUST be awaited before `setStorageAccount(userId)`, which refuses an account it was not hydrated for. `AuthContext` is the one caller of both. An account-scoped access before that throws. A module that MIRRORS an account-scoped key in memory subscribes to `onStorageAccountChange` so the mirror moves with the namespace.

Cross-tab changes travel on a BroadcastChannel (IndexedDB raises no event), delivered by `onStorageChanged` as the set of stored keys that moved.

Adding a new key:

1. Add the constant (`KEY_*`) or the prefix (`PREFIX_*`) to `browserStorage.ts`.
2. Register it in `LOCAL_KEY_SPECS` or `SESSION_KEY_SPECS`. `satisfies` turns a missing `scope` — or, for a local key, a missing `access` — into a compile error.
3. Read and write through the helpers for its tier. They throw for an unregistered name and for the wrong tier, so a mistake fails visibly instead of disappearing on the next sweep.

Two guards enforce what the types cannot: `no-restricted-globals` / `no-restricted-properties` in `eslint.config.ts` reject any reference to the storage globals outside the gateway (and any `dexie` import outside `~/lib/idb`), and `src/test-support/storageKeysAreRegistered.test.ts` fails the suite for an exported key constant that neither table registers, and for a name registered in both.

## Git

Never commit generated files. Output under `generated/` directories (sqlc, proto stubs, etc.) is gitignored — exclude anything generated when staging.
