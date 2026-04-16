/**
 * Centralized browser localStorage management.
 *
 * All localStorage keys must be registered here (STATIC_KEYS or DYNAMIC_KEY_TTLS).
 * Dynamic keys (per-agent/per-worker) are wrapped as { v: T, e: number }
 * with an expiration timestamp and cleaned up automatically. Static keys
 * (preferences, key-pins) are stored raw and never expire.
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type EnterKeyMode = 'enter-sends' | 'cmd-enter-sends'

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
}

// ---------------------------------------------------------------------------
// Key registry
// ---------------------------------------------------------------------------

/** Static key constants — single source of truth for all consumers. */
export const KEY_BROWSER_PREFS = 'leapmux:browser-prefs'
export const KEY_MRU_AGENT_PROVIDERS = 'leapmux:mru-agent-providers'
export const KEY_KEY_PINS = 'leapmux:key-pins'
export const KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN = 'leapmux:directory-selector-show-hidden'

/** Keys that are never cleaned up and stored without wrapping. */
export const STATIC_KEYS = new Set([
  KEY_BROWSER_PREFS,
  KEY_MRU_AGENT_PROVIDERS,
  KEY_KEY_PINS,
  KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN,
])

/** Dynamic key prefixes — single source of truth for all consumers. */
export const PREFIX_EDITOR_DRAFT = 'leapmux:editor-draft:'
export const PREFIX_EDITOR_MIN_HEIGHT = 'leapmux:editor-min-height:'
export const PREFIX_AGENT_SESSION = 'leapmux:agent-session:'
export const PREFIX_ASK_STATE = 'leapmux:ask-state:'
export const PREFIX_WORKER_INFO = 'leapmux:worker-info:'
export const PREFIX_LOCAL_MESSAGES = 'leapmux:local-messages:'
export const PREFIX_FILES_SHOW_HIDDEN = 'leapmux:files-show-hidden:'

const DAY_MS = 24 * 60 * 60 * 1000
const HOUR_MS = 60 * 60 * 1000
const REFRESH_THRESHOLD_MS = 3 * HOUR_MS
const CLEANUP_INTERVAL_MS = HOUR_MS

/** Dynamic key prefixes and their TTLs. */
export const DYNAMIC_KEY_TTLS: ReadonlyArray<{ prefix: string, ttlMs: number }> = [
  { prefix: PREFIX_EDITOR_DRAFT, ttlMs: 7 * DAY_MS },
  { prefix: PREFIX_EDITOR_MIN_HEIGHT, ttlMs: 7 * DAY_MS },
  { prefix: PREFIX_AGENT_SESSION, ttlMs: 7 * DAY_MS },
  { prefix: PREFIX_ASK_STATE, ttlMs: 1 * DAY_MS },
  { prefix: PREFIX_WORKER_INFO, ttlMs: 7 * DAY_MS },
  { prefix: PREFIX_LOCAL_MESSAGES, ttlMs: 7 * DAY_MS },
  { prefix: PREFIX_FILES_SHOW_HIDDEN, ttlMs: 7 * DAY_MS },
]

// ---------------------------------------------------------------------------
// Key helpers
// ---------------------------------------------------------------------------

/** Returns the TTL in ms for a dynamic key, or null if the key is static or unknown. */
export function getTtlForKey(key: string): number | null {
  for (const { prefix, ttlMs } of DYNAMIC_KEY_TTLS) {
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
// Browser preferences
// ---------------------------------------------------------------------------

/** Load the consolidated browser preferences from localStorage. */
export function loadBrowserPrefs(): BrowserPreferences {
  try {
    const raw = localStorage.getItem(KEY_BROWSER_PREFS)
    if (raw !== null)
      return JSON.parse(raw) as BrowserPreferences
  }
  catch { /* ignore parse errors */ }
  return {}
}

// ---------------------------------------------------------------------------
// Safe localStorage wrappers
// ---------------------------------------------------------------------------

/**
 * Validate the key and return its TTL. Throws if the key is not registered.
 * Returns null for static keys, the TTL in ms for dynamic keys.
 */
function requireKnownKey(key: string): number | null {
  const ttl = getTtlForKey(key)
  if (ttl !== null)
    return ttl
  if (!STATIC_KEYS.has(key)) {
    throw new Error(
      `Unknown localStorage key: "${key}". Register it in browserStorage.ts `
      + `(STATIC_KEYS or DYNAMIC_KEY_TTLS).`,
    )
  }
  return null
}

/**
 * Read and unwrap a dynamic key's value, handling expiration and refresh.
 * Returns the unwrapped value, or undefined if missing/expired/malformed.
 */
function readDynamic(key: string, ttl: number): unknown | undefined {
  const raw = localStorage.getItem(key)
  if (raw === null)
    return undefined

  const parsed = JSON.parse(raw)
  if (!isWrappedValue(parsed))
    return undefined

  if (parsed.e <= Date.now()) {
    localStorage.removeItem(key)
    return undefined
  }

  if (shouldRefreshExpiration(parsed.e, ttl)) {
    parsed.e = Date.now() + ttl
    localStorage.setItem(key, JSON.stringify(parsed))
  }

  return parsed.v
}

/** Read and parse a JSON value from localStorage. Returns undefined on missing key or parse error. */
export function safeGetJson<T>(key: string): T | undefined {
  const ttl = requireKnownKey(key)
  try {
    if (ttl !== null)
      return readDynamic(key, ttl) as T | undefined

    const raw = localStorage.getItem(key)
    if (raw === null)
      return undefined
    return JSON.parse(raw) as T
  }
  catch { /* ignore parse errors */ }
  return undefined
}

/** Stringify and write a JSON value to localStorage. Silently ignores write errors. */
export function safeSetJson(key: string, value: unknown): void {
  const ttl = requireKnownKey(key)
  try {
    if (ttl !== null) {
      localStorage.setItem(key, JSON.stringify({ v: value, e: Date.now() + ttl }))
    }
    else {
      localStorage.setItem(key, JSON.stringify(value))
    }
  }
  catch { /* ignore write errors (e.g. quota exceeded) */ }
}

/** Read a raw string from localStorage. Returns null on missing key or access error. */
export function safeGetString(key: string): string | null {
  const ttl = requireKnownKey(key)
  try {
    if (ttl !== null) {
      const v = readDynamic(key, ttl)
      return v !== undefined ? String(v) : null
    }
    return localStorage.getItem(key)
  }
  catch { /* ignore access errors */ }
  return null
}

/** Write a raw string to localStorage. Silently ignores write errors. */
export function safeSetString(key: string, value: string): void {
  const ttl = requireKnownKey(key)
  try {
    if (ttl !== null) {
      localStorage.setItem(key, JSON.stringify({ v: value, e: Date.now() + ttl }))
    }
    else {
      localStorage.setItem(key, value)
    }
  }
  catch { /* ignore write errors */ }
}

/** Remove a key from localStorage. Silently ignores errors. */
export function safeRemoveItem(key: string): void {
  try {
    localStorage.removeItem(key)
  }
  catch { /* ignore errors */ }
}

// ---------------------------------------------------------------------------
// Cleanup
// ---------------------------------------------------------------------------

/**
 * Scan localStorage and delete any `leapmux:` or `leapmux-` key that is
 * NOT a known static key AND NOT a valid non-expired wrapped dynamic key.
 */
export function runCleanup(): void {
  const now = Date.now()
  const keysToDelete: string[] = []
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i)
    if (!key)
      continue
    if (!key.startsWith('leapmux:') && !key.startsWith('leapmux-'))
      continue

    if (STATIC_KEYS.has(key))
      continue

    const ttl = getTtlForKey(key)
    if (ttl !== null) {
      try {
        const raw = localStorage.getItem(key)
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
      localStorage.removeItem(key)
    }
    catch { /* ignore removal errors */ }
  }
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
