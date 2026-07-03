/**
 * Centralized browser storage management.
 *
 * Every `leapmux:`-family key is registered in one of four registries:
 *   - `EXACT_KEY_TTLS`         (localStorage,   exact match)
 *   - `DYNAMIC_KEY_TTLS`       (localStorage,   prefix match)
 *   - `SESSION_EXACT_KEY_TTLS` (sessionStorage, exact match)
 *   - `SESSION_DYNAMIC_KEY_TTLS` (sessionStorage, prefix match)
 *
 * Every value is wrapped as `{ v: T, e: number }` with an expiration
 * timestamp; reads unwrap and may refresh the timestamp on access.
 * Long-lived preferences use a 1-year TTL plus the refresh-on-read
 * mechanism, so opening the app at any point in a year keeps them
 * alive; total inactivity for a year is the only way they expire.
 *
 * `runCleanup` sweeps both stores on a timer, deleting any
 * `leapmux:`-family key whose wrapper is missing/malformed/expired.
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type EnterKeyMode = 'enter-sends' | 'cmd-enter-sends'
export type TerminalRendererPreference = 'auto' | 'webgl' | 'canvas'

/**
 * Browser-level preferences stored as a single JSON object.
 * Fields that are undefined mean "use account default."
 * Schema mirrors the backend storedPreferences struct.
 */
export interface BrowserPreferences {
  theme?: string
  terminalTheme?: string
  diffView?: string
  turnEndSound?: string
  turnEndSoundVolume?: number
  debugLogging?: boolean
  expandAgentThoughts?: boolean
  showHiddenMessages?: boolean
  enterKeyMode?: EnterKeyMode
  terminalRenderer?: TerminalRendererPreference
  /**
   * When true, ignore `ActiveTabRequested` events from other clients
   * (e.g. `leapmux remote tab focus`). Default false: another browser
   * tab or the CLI can steal focus, which is the intended behaviour
   * for "tab focus" to "just work" — flip this if your workflow
   * dislikes remote-driven focus changes.
   */
  ignoreRemoteFocus?: boolean
  /**
   * Whether to reveal the saved file in the OS file manager (Finder /
   * Explorer / Files) after a successful download. Only applies in
   * desktop mode; ignored in the browser. Defaults to true — set to
   * `false` explicitly to opt out.
   */
  revealAfterDownload?: boolean
}

// ---------------------------------------------------------------------------
// Key registry
// ---------------------------------------------------------------------------

/** Long-lived localStorage singletons (exact-match in the TTL registry). */
export const KEY_BROWSER_PREFS = 'leapmux:browser-prefs'
export const KEY_MRU_AGENT_PROVIDERS = 'leapmux:mru-agent-providers'
export const KEY_KEY_PINS = 'leapmux:key-pins'
export const KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN = 'leapmux:directory-selector-show-hidden'
export const KEY_PREFERRED_EDITOR = 'leapmux:preferred-editor'

/** Dynamic key prefixes — single source of truth for all consumers. */
export const PREFIX_EDITOR_DRAFT = 'leapmux:editor-draft:'
export const PREFIX_EDITOR_MIN_HEIGHT = 'leapmux:editor-min-height:'
export const PREFIX_AGENT_SESSION = 'leapmux:agent-session:'
export const PREFIX_ASK_STATE = 'leapmux:ask-state:'
export const PREFIX_WORKER_INFO = 'leapmux:worker-info:'
export const PREFIX_LOCAL_MESSAGES = 'leapmux:local-messages:'
export const PREFIX_FILES_SHOW_HIDDEN = 'leapmux:files-show-hidden:'
export const PREFIX_CHAT_ROW_HEIGHTS = 'leapmux:chat-row-heights:'

/** sessionStorage dynamic key prefixes. */
export const PREFIX_FILE_SCROLL = 'leapmux:fileScroll:'
export const PREFIX_ACTIVE_TAB = 'leapmux:activeTab:'
export const PREFIX_TILE_ACTIVE_TABS = 'leapmux:tileActiveTabs:'
export const PREFIX_FOCUSED_TILE = 'leapmux:focusedTile:'
export const PREFIX_SIDEBAR = 'leapmux:sidebar:'
export const PREFIX_TAB_TREE = 'leapmux:tabTree:'
export const PREFIX_DIRECTORY_TREE = 'leapmux:directoryTree:'
/** Singleton sessionStorage keys (exact-match in the TTL registry). */
export const KEY_ACTIVE_WORKSPACE = 'leapmux:activeWorkspace'
export const KEY_CLI_PATH_CHECKED = 'leapmux:cli-path-checked'
export const KEY_EXPANDED_WORKSPACES = 'leapmux:expandedWorkspaces'
export const KEY_CLIENT_ID = 'leapmux:client-id'

const DAY_MS = 24 * 60 * 60 * 1000
const HOUR_MS = 60 * 60 * 1000
const REFRESH_THRESHOLD_MS = 3 * HOUR_MS
const CLEANUP_INTERVAL_MS = HOUR_MS
const YEAR_MS = 365 * DAY_MS

/**
 * Singleton localStorage keys (matched by exact string). User-level
 * preferences and trust state — values that should outlive ordinary
 * idle gaps but still self-clean if the app goes unopened for a year.
 * The on-read refresh in `readDynamic` pushes the expiration forward
 * on every access, so a user who opens the app at any point during
 * the year keeps these forever; a year of total inactivity expires
 * them.
 */
export const EXACT_KEY_TTLS: ReadonlyMap<string, number> = new Map([
  [KEY_BROWSER_PREFS, YEAR_MS],
  [KEY_MRU_AGENT_PROVIDERS, YEAR_MS],
  [KEY_KEY_PINS, YEAR_MS],
  [KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN, YEAR_MS],
  [KEY_PREFERRED_EDITOR, YEAR_MS],
])

/** Dynamic key prefixes and their TTLs (localStorage). */
export const DYNAMIC_KEY_TTLS: ReadonlyArray<{ prefix: string, ttlMs: number }> = [
  { prefix: PREFIX_EDITOR_DRAFT, ttlMs: 7 * DAY_MS },
  { prefix: PREFIX_EDITOR_MIN_HEIGHT, ttlMs: 7 * DAY_MS },
  { prefix: PREFIX_AGENT_SESSION, ttlMs: 7 * DAY_MS },
  { prefix: PREFIX_ASK_STATE, ttlMs: 1 * DAY_MS },
  { prefix: PREFIX_WORKER_INFO, ttlMs: 7 * DAY_MS },
  { prefix: PREFIX_LOCAL_MESSAGES, ttlMs: 7 * DAY_MS },
  { prefix: PREFIX_FILES_SHOW_HIDDEN, ttlMs: 7 * DAY_MS },
  // Measured chat-row heights (see chatRowHeightPersistence). A warm-start
  // cache: stale entries are harmless (each row's key digest must match its
  // live heightKey to hydrate), so the TTL only bounds storage growth.
  { prefix: PREFIX_CHAT_ROW_HEIGHTS, ttlMs: 7 * DAY_MS },
]

/**
 * Templated sessionStorage keys (matched by `startsWith` against the
 * prefix). sessionStorage normally clears on tab close, but PWAs and
 * "restore tabs on restart" can keep it alive across sessions —
 * capping retention bounds the key set without depending on tab-close
 * cleanup.
 *
 * Per-workspace UI state (active tab, tile active tabs, focused tile,
 * sidebar layout, tab-tree group collapse, directory-tree expansion)
 * is restored by `useWorkspaceRestore` on page refresh. Without
 * registration the on-load sweep wipes these and the restore path
 * falls back to "activate the first tab" / "navigate to the first
 * workspace". 30 days lets a user return after a long break and still
 * land on their last tab.
 */
export const SESSION_DYNAMIC_KEY_TTLS: ReadonlyArray<{ prefix: string, ttlMs: number }> = [
  { prefix: PREFIX_FILE_SCROLL, ttlMs: 1 * DAY_MS },
  { prefix: PREFIX_ACTIVE_TAB, ttlMs: 30 * DAY_MS },
  { prefix: PREFIX_TILE_ACTIVE_TABS, ttlMs: 30 * DAY_MS },
  { prefix: PREFIX_FOCUSED_TILE, ttlMs: 30 * DAY_MS },
  { prefix: PREFIX_SIDEBAR, ttlMs: 30 * DAY_MS },
  { prefix: PREFIX_TAB_TREE, ttlMs: 30 * DAY_MS },
  { prefix: PREFIX_DIRECTORY_TREE, ttlMs: 30 * DAY_MS },
]

/**
 * Singleton sessionStorage keys (matched by exact string). Separating
 * these from the prefix-match table means a future key whose name
 * accidentally begins with one of these strings can't silently inherit
 * its TTL — every singleton has to be registered by its exact value.
 *
 * - `KEY_ACTIVE_WORKSPACE` / `KEY_EXPANDED_WORKSPACES`: the workspace
 *   currently active and the set of expanded workspaces in the sidebar
 *   tree. Match the 30-day lifetime of the per-workspace UI snapshot.
 * - `KEY_CLIENT_ID`: per-session CRDT client identity. Long-lived so a
 *   refresh keeps the same id; the TTL bounds retention if the tab
 *   survives for weeks without being closed.
 * - `KEY_CLI_PATH_CHECKED`: one-shot gate for the macOS "install
 *   leapmux on PATH" prompt. At most once per session; the TTL is a
 *   backstop in case sessionStorage is preserved across sessions.
 */
export const SESSION_EXACT_KEY_TTLS: ReadonlyMap<string, number> = new Map([
  [KEY_ACTIVE_WORKSPACE, 30 * DAY_MS],
  [KEY_EXPANDED_WORKSPACES, 30 * DAY_MS],
  [KEY_CLIENT_ID, 30 * DAY_MS],
  [KEY_CLI_PATH_CHECKED, 1 * DAY_MS],
])

// ---------------------------------------------------------------------------
// Key helpers
// ---------------------------------------------------------------------------

/** Returns the TTL in ms for a registered localStorage key, or null if unknown. */
export function getTtlForKey(key: string): number | null {
  const exact = EXACT_KEY_TTLS.get(key)
  if (exact !== undefined)
    return exact
  for (const { prefix, ttlMs } of DYNAMIC_KEY_TTLS) {
    if (key.startsWith(prefix))
      return ttlMs
  }
  return null
}

/** Returns the TTL in ms for a registered sessionStorage key, or null if unknown. */
export function getSessionTtlForKey(key: string): number | null {
  const exact = SESSION_EXACT_KEY_TTLS.get(key)
  if (exact !== undefined)
    return exact
  for (const { prefix, ttlMs } of SESSION_DYNAMIC_KEY_TTLS) {
    if (key.startsWith(prefix))
      return ttlMs
  }
  return null
}

/** Type guard: checks if a parsed value has the wrapped format { v, e }. */
export function isWrappedValue(raw: unknown): raw is { v: unknown, e: number } {
  return (
    typeof raw === 'object'
    && raw !== null
    && !Array.isArray(raw)
    && 'v' in raw
    && 'e' in raw
    && typeof (raw as Record<string, unknown>).e === 'number'
  )
}

/**
 * Check if a wrapped value's expiration should be refreshed on read.
 * Returns true if the expiration was last refreshed more than 3 hours ago
 * (i.e. the remaining lifetime is shorter than TTL minus 3 hours).
 */
export function shouldRefreshExpiration(e: number, ttlMs: number): boolean {
  return e < Date.now() + ttlMs - REFRESH_THRESHOLD_MS
}

// ---------------------------------------------------------------------------
// Safe localStorage wrappers
// ---------------------------------------------------------------------------

function requireKnownKey(key: string): number {
  const ttl = getTtlForKey(key)
  if (ttl === null) {
    throw new Error(
      `Unknown localStorage key: "${key}". Register it in browserStorage.ts `
      + `(EXACT_KEY_TTLS for singletons, DYNAMIC_KEY_TTLS for templated keys).`,
    )
  }
  return ttl
}

/**
 * Read and unwrap a dynamic key's value, handling expiration and refresh.
 * Returns the unwrapped value, or undefined if missing/expired/malformed.
 */
function readDynamic(storage: Storage, key: string, ttl: number): unknown | undefined {
  const raw = storage.getItem(key)
  if (raw === null)
    return undefined

  const parsed = JSON.parse(raw)
  if (!isWrappedValue(parsed))
    return undefined

  if (parsed.e <= Date.now()) {
    storage.removeItem(key)
    return undefined
  }

  if (shouldRefreshExpiration(parsed.e, ttl)) {
    parsed.e = Date.now() + ttl
    storage.setItem(key, JSON.stringify(parsed))
  }

  return parsed.v
}

/** Write a value wrapped with a TTL expiration to `storage`. */
function writeWrapped(storage: Storage, key: string, value: unknown, ttl: number): void {
  storage.setItem(key, JSON.stringify({ v: value, e: Date.now() + ttl }))
}

/** Read and unwrap a value from localStorage. Returns undefined on missing/expired/malformed. */
export function localStorageGet<T>(key: string): T | undefined {
  const ttl = requireKnownKey(key)
  try {
    return readDynamic(localStorage, key, ttl) as T | undefined
  }
  catch { /* ignore parse errors */ }
  return undefined
}

/** Stringify and write a value to localStorage wrapped with a TTL. Silently ignores write errors. */
export function localStorageSet(key: string, value: unknown): void {
  const ttl = requireKnownKey(key)
  try {
    writeWrapped(localStorage, key, value, ttl)
  }
  catch { /* ignore write errors (e.g. quota exceeded) */ }
}

/** Remove a key from localStorage. Silently ignores errors. */
export function localStorageRemove(key: string): void {
  try {
    localStorage.removeItem(key)
  }
  catch { /* ignore errors */ }
}

/** Load the consolidated browser preferences from localStorage. */
export function loadBrowserPrefs(): BrowserPreferences {
  return localStorageGet<BrowserPreferences>(KEY_BROWSER_PREFS) ?? {}
}

// ---------------------------------------------------------------------------
// Safe sessionStorage wrappers
// ---------------------------------------------------------------------------

function requireKnownSessionKey(key: string): number {
  const ttl = getSessionTtlForKey(key)
  if (ttl === null) {
    throw new Error(
      `Unknown sessionStorage key: "${key}". Register it in browserStorage.ts `
      + `(SESSION_EXACT_KEY_TTLS for singletons, SESSION_DYNAMIC_KEY_TTLS for templated keys).`,
    )
  }
  return ttl
}

/** Read and unwrap a value from sessionStorage. Returns undefined on missing/expired/malformed. */
export function sessionStorageGet<T>(key: string): T | undefined {
  const ttl = requireKnownSessionKey(key)
  try {
    return readDynamic(sessionStorage, key, ttl) as T | undefined
  }
  catch { /* ignore parse errors */ }
  return undefined
}

/** Stringify and write a value to sessionStorage wrapped with a TTL. Silently ignores write errors. */
export function sessionStorageSet(key: string, value: unknown): void {
  const ttl = requireKnownSessionKey(key)
  try {
    writeWrapped(sessionStorage, key, value, ttl)
  }
  catch { /* ignore write errors */ }
}

/**
 * Cheap existence check: true iff the key has any value in sessionStorage.
 * Skips the wrapper parse / TTL refresh that `sessionStorageGet` performs —
 * use this when callers only need "did anything write here?".
 */
export function sessionStorageHas(key: string): boolean {
  requireKnownSessionKey(key)
  try {
    return sessionStorage.getItem(key) !== null
  }
  catch { /* ignore access errors */ }
  return false
}

/** Remove a key from sessionStorage. Silently ignores errors. */
export function sessionStorageRemove(key: string): void {
  try {
    sessionStorage.removeItem(key)
  }
  catch { /* ignore errors */ }
}

// ---------------------------------------------------------------------------
// Cleanup
// ---------------------------------------------------------------------------

function sweepStorage(
  storage: Storage,
  ttlFor: (key: string) => number | null,
): void {
  const now = Date.now()
  const keysToDelete: string[] = []
  for (let i = 0; i < storage.length; i++) {
    const key = storage.key(i)
    if (!key)
      continue
    if (!key.startsWith('leapmux:') && !key.startsWith('leapmux-'))
      continue
    if (ttlFor(key) !== null) {
      try {
        const raw = storage.getItem(key)
        if (raw !== null) {
          const parsed = JSON.parse(raw)
          if (isWrappedValue(parsed) && parsed.e > now)
            continue
        }
      }
      catch { /* parse error → treat as stale */ }
    }
    keysToDelete.push(key)
  }
  for (const key of keysToDelete) {
    try {
      storage.removeItem(key)
    }
    catch { /* ignore removal errors */ }
  }
}

/**
 * Scan localStorage and sessionStorage and delete every `leapmux:`-family
 * key that is unregistered or whose wrapper is missing / malformed /
 * expired.
 */
export function runCleanup(): void {
  sweepStorage(localStorage, getTtlForKey)
  sweepStorage(sessionStorage, getSessionTtlForKey)
}

/**
 * Initialize the storage cleanup system.
 * Runs cleanup immediately, then on an hourly interval.
 * Returns a dispose function that clears the interval.
 */
export function initStorageCleanup(): () => void {
  runCleanup()
  const id = setInterval(runCleanup, CLEANUP_INTERVAL_MS)
  return () => clearInterval(id)
}
