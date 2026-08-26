# LeapMux

Multi-agent coding assistant platform supporting Claude Code and Codex.

- Backend: Go
- Frontend: SolidJS with vanilla-extract CSS (`.css.ts` files)
- E2E: Playwright
- Desktop: Tauri (Rust + Go sidecar)

## Build system

Use `task` (`Taskfile.yaml`) targets, not the underlying tools directly.

- Frontend package manager: `bun` (lock: `bun.lock`)
- Proto generation: `buf generate` (via `task generate-proto`)
- SQL generation: `sqlc generate` (via `task generate-sqlc`)

### vanilla-extract `.css.ts` files

Never write a bare `*.css.ts` basename inside a `.css.ts` file — not in code, and not in a comment. Write `~/styles/global.css.ts` or `./widgets/SpanLines.css.ts`, never `global.css.ts`. A bare basename makes the vanilla-extract compiler fail the whole module with `Styles were unable to be assigned to a file`, pointing at an unrelated line in that file. Every test that imports the module then fails to load, which reads as a broken component rather than a broken comment.

### sqlc files

`backend/internal/hub/store/{sqlite,postgres,mysql}/db/queries/*.sql` and any other sqlc query files MUST contain only ASCII characters. The sqlc parser falls over on non-ASCII bytes (typically inside comments) with a misleading `mismatched input 'SELECr'`-style error that points at the wrong line. Use `--` (double hyphen) and plain ASCII punctuation instead of `—` (em-dash) or smart quotes.

## Common commands

- `task generate` — proto + sqlc generation
- `task build` — full build (backend + frontend)
- `task lint` — all linters
- `task test` — all tests
- `task test-e2e -- <files>` — run only the affected E2E specs, not the full suite
- `task lint-backend` / `task lint-frontend` / `task lint-desktop`
- `task test-backend` / `task test-frontend`

Lint Rust/desktop code with `task lint-desktop`, not `cargo clippy` directly. The task builds the Go sidecar binary first, which Tauri's bundle resources reference at `../go/bin/*`. Running `cargo clippy` directly fails with a misleading build error.

## Coding conventions

### Provider-specific logic belongs in the provider, not shared code

LeapMux supports many agent providers (Claude Code, Codex, Pi, and ACP-based
providers: OpenCode, Cursor, Copilot, Kilo, Goose, Reasonix). Anything that depends
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
every other provider and is a second source of truth that drifts. When you catch
yourself writing a provider's tool/method name outside its plugin, move it into the
plugin behind an interface method.

### Tests

- Backend: `testify/assert`, `testify/require`.
- Frontend: `vitest`. `describe` names must not be Title Case — start them lowercase (`describe('parses empty input')`). Naming one after the symbol under test keeps that symbol's own casing: `describe('createStableContext')`, `describe('channelManager openChannel')`. Never start a `describe` or `it` name with a word that must keep its capital, because the lint autofix lowercases the first letter alone and misspells it (`dEFAULT_MONO_FONT_FAMILY`). Lead with a lowercase phrase instead: `describe('default mono font stack (DEFAULT_MONO_FONT_FAMILY)')` (see `src/test-support/noMangledTestTitles.test.ts`, which fails the suite on a mangled title).
- **Unit tests are co-located** with the code they test: `foo.ts` → `foo.test.ts` in the same directory. This holds under `tests/e2e/` too — an E2E helper carries its own `.test.ts` beside it (`helpers/mail.ts` → `helpers/mail.test.ts`). Do **not** add a second test file for a module under `tests/unit/` — that mirror has been retired (see `src/test-support/noMirroredUnitTests.test.ts`, which fails the suite if it comes back). Shared unit-test helpers live in `src/test-support/` (imported via `~/test-support/…`).
- **The file extension picks the runner**, everywhere: `.spec.ts` is Playwright, `.test.ts` is vitest. So a `.test.ts` under `tests/e2e/helpers/` runs in `task test-frontend` — no browser, no hub, milliseconds — and never in the E2E suite. Both configs are pinned to this (`vitest.config.ts` excludes `tests/e2e/**/*.spec.ts` by name, not `tests/e2e/**`; `playwright.config.ts` sets `testMatch: '**/*.spec.ts'`), and `src/test-support/testFileNaming.test.ts` fails the suite when a file lands on the wrong side or a config stops enforcing its half. Do not widen the vitest exclude back to `tests/e2e/**`: with Playwright pinned to `.spec.ts`, a co-located test would then run under **neither** runner. Playwright's own default `testMatch` takes `*.test.ts` as well, which is why the pin is there — without it those tests run in a browser worker, where vitest's API does not exist.
- E2E: do NOT pass per-call `{ timeout: … }` overrides to `expect`, `locator.waitFor`, etc. Playwright's global timeout (configured in `playwright.config.ts`) already applies; per-call overrides are redundant noise. If a specific assertion legitimately needs a longer-than-global timeout (e.g. waiting on a slow worker spawn), discuss it before silently adding one.
- Unused imports cause lint failures (strict).
- Test provider-specific logic in that provider's test file (e.g. Claude's `previewText` in `providers/claude/plugin.test.ts`), not in a shared module's test.

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

Use the `<Tooltip>` component (`~/components/common/Tooltip`) for hover text on an interactive element. Do NOT reach for a bare `title` attribute — it renders the OS tooltip, which ignores the app's theme and typography, appears after a browser-controlled delay, and is invisible on touch.

```tsx
<Tooltip text="Remove link, keeping the text" ariaLabel>
  <button onClick={remove}><Icon icon={Trash2} size="xs" /></button>
</Tooltip>
```

Pass `ariaLabel` when the control has no visible text, so the tooltip doubles as its accessible name.

**`title` on a DOM element is a lint error** (`no-restricted-syntax` in `eslint.config.ts`). There is no exception, including a **disabled** control: `<Tooltip>` covers that case. It gives its wrapper a real box and listens there — a disabled element dispatches no pointer event of its own — and it leaves an offscreen description in `aria-describedby` for as long as the control is disabled, which is the only route to a screen-reader user there (a disabled element takes no focus, so the tooltip can never open from the keyboard).

Two things go wrong with a native `title`, and the second is silent. It renders the OS tooltip, which ignores the app's theme and typography, waits a browser-controlled delay, and never appears on touch. And on a control with no `aria-label`, a `title` long enough to state a reason **becomes the accessible name** — a screen reader then announces three sentences of remedy where "Add passkey" belongs, and every `getByRole(..., { name })` lookup stops matching.

The lint rule matches a **lowercase** element name only, because `title` on a component is that component's own prop: `<Dialog title>` is a heading, `<IconButton title>` is a tooltip. A component that spreads its props onto a DOM node closes that hole in the type system instead, by omitting `title` from its prop type — `IconButton` and `ConfirmButton` both do, and a new one that spreads DOM props must.

### Dropdowns and one-of-N choices

Never render a native `<select>`. Use:

- `<PillGroup>` (`~/components/common/PillGroup`) for a short fixed set — up to
  four options that fit on one row. It supplies `role="radiogroup"`, roving
  tabindex and the arrow-key contract.
- `<DropdownMenu>` + `<DropdownMenuCheckableItem kind="radio">`
  (`~/components/common/DropdownMenu`) for anything longer, dynamic or
  unbounded. Follow `AgentProviderSelector` and `PreferencesNav`, which already
  do this.

Why: a native `<select>` opens the OS picker, which ignores the app's theme and
typography — the same reason a bare `title` is banned for tooltips. It renders
text and nothing else, so a colour swatch, an icon or a second line is
impossible; `ThemeChooser` needs exactly that. And its selected index is browser
state, so every caller ends up repairing the DOM by hand after a refused write
or an option-list swap — two such repairs were deleted when the last selects
went. A menu derives from props and cannot drift.

For an unbounded list, give the menu a filter box: render it `as="div"` so a
click inside does not dismiss it, and close from the item's own handler.

### Browser storage

Never call `localStorage` or `sessionStorage` directly. Route every read, write, and delete through `~/lib/browserStorage` (`localStorageGet`/`localStorageSet`/`localStorageRemove` for localStorage; `sessionStorageGet`/`sessionStorageSet`/`sessionStorageHas`/`sessionStorageRemove` for sessionStorage). A test resets a store with `localStorageClearForTests` / `sessionStorageClearForTests`, not with `clear()`.

Callers pass a LOGICAL name (`'key-pins'`, `'worker-info:w-1'`). The module owns the physical layout and composes the whole stored key, so no call site builds one by hand.

Why: every key is scoped to one account, and every value is wrapped as `{ v, e }` with an expiration timestamp. Reads unwrap the value and refresh the timestamp, so a key stays alive as long as the app is touched within its TTL. `runCleanup` sweeps both stores and deletes any `leapmux:`-family key that is unregistered, that carries a scope its registration does not allow, or whose wrapper is missing, malformed, or expired. It KEEPS another account's fresh key, which is the point of the scope. A raw `setItem(...)` write skips both the scope and the envelope, so a second account on the browser reads it and the next sweep deletes it.

Two registries hold every key, by logical name:

- `LOCAL_KEY_SPECS` — localStorage.
- `SESSION_KEY_SPECS` — sessionStorage.

Each entry states `match` (`exact` or `prefix`), `scope` and `ttlMs`. A `scope: 'account'` key is stored at `leapmux:u:<userId>:<name>`, and that is the answer for anything a user owns. A `scope: 'device'` key is stored at `leapmux:<name>`, for state that fences a resource shared by every account on the origin; the two relay sequence marks are the only entries today.

`setStorageAccount(userId)` points the account namespace at the signed-in user, and `AuthContext` is its one caller. An account-scoped access before that call throws. A module that MIRRORS an account-scoped key in memory subscribes to `onStorageAccountChange` so the mirror moves with the namespace.

Adding a new key:

1. Add the constant (`KEY_*`) or the prefix (`PREFIX_*`) to `browserStorage.ts`.
2. Register it in `LOCAL_KEY_SPECS` or `SESSION_KEY_SPECS` with a `match`, a `scope` and a TTL. `satisfies Record<string, KeySpec>` turns a missing `scope` into a compile error.
3. Read and write through the helpers. They throw for an unregistered name, so a missed registration fails loudly instead of disappearing on the next sweep.

Two guards enforce what the types cannot: `no-restricted-globals` in `eslint.config.ts` rejects any reference to the storage globals outside the gateway, and `src/test-support/storageKeysAreRegistered.test.ts` fails the suite for an exported key constant that neither table registers, and for a name registered in both.

## Git

Never commit generated files. Output under `generated/` directories (sqlc, proto stubs, etc.) is gitignored — exclude anything generated when staging.
