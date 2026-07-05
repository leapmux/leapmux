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
- Frontend: `vitest`. `describe` names must be lowercase.
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

### Browser storage

Never call `localStorage` or `sessionStorage` directly. Route every read, write, and delete through `~/lib/browserStorage` (`localStorageGet`/`localStorageSet`/`localStorageRemove` for localStorage; `sessionStorageGet`/`sessionStorageSet`/`sessionStorageHas`/`sessionStorageRemove` for sessionStorage).

Why: every `leapmux:`-prefixed key is swept on each page load by `runCleanup` in `~/lib/browserStorage`. Every value is wrapped as `{ v, e }` with an expiration timestamp; reads unwrap and refresh the timestamp on access, so a key stays alive as long as the app is touched within its TTL. The sweep deletes any `leapmux:`-family key whose wrapper is missing, malformed, or expired — a raw `setItem(...)` write survives the current page but is wiped on the next refresh.

Four registries divide keys by storage and match-type:

- `EXACT_KEY_TTLS` — localStorage, exact match. Long-lived singletons (prefs, key-pins).
- `DYNAMIC_KEY_TTLS` — localStorage, prefix match. Templated per-feature keys.
- `SESSION_EXACT_KEY_TTLS` — sessionStorage, exact match.
- `SESSION_DYNAMIC_KEY_TTLS` — sessionStorage, prefix match.

Adding a new key:

1. Add the constant (`KEY_*`) or prefix (`PREFIX_*`) to `browserStorage.ts` and register it in the right table with a TTL.
2. Read/write through the helpers. They throw at write time if the key isn't registered, so a missed registration fails loudly instead of silently disappearing on the next reload.

## Git

Never commit generated files. Output under `generated/` directories (sqlc, proto stubs, etc.) is gitignored — exclude anything generated when staging.
