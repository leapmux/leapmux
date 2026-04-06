/**
 * Centralized localStorage cleanup system with TTL-based expiration.
 *
 * Dynamic keys (per-agent/per-worker) are wrapped as { v: T, e: number }
 * where `e` is the expiration timestamp. Static keys (preferences) are
 * stored raw and never expire.
 */

/** Keys that are never cleaned up and stored without wrapping. */
export const STATIC_KEYS = new Set([
  'leapmux:theme',
  'leapmux:terminal-theme',
  'leapmux:diff-view',
  'leapmux:turn-end-sound',
  'leapmux:turn-end-sound-volume',
  'leapmux:debug-logging',
  'leapmux:show-hidden-messages',
  'leapmux:enter-key-mode',
  'leapmux:mru-agent-providers',
  'leapmux:key-pins',
])

const DAY_MS = 24 * 60 * 60 * 1000
const HOUR_MS = 60 * 60 * 1000
const REFRESH_THRESHOLD_MS = 3 * HOUR_MS
const CLEANUP_INTERVAL_MS = HOUR_MS

/** Dynamic key prefixes and their TTLs. */
export const DYNAMIC_KEY_TTLS: ReadonlyArray<{ prefix: string, ttlMs: number }> = [
  { prefix: 'leapmux:editor-draft:', ttlMs: 7 * DAY_MS },
  { prefix: 'leapmux:editor-min-height:', ttlMs: 7 * DAY_MS },
  { prefix: 'leapmux:agent-session:', ttlMs: 7 * DAY_MS },
  { prefix: 'leapmux:ask-state:', ttlMs: 1 * DAY_MS },
  { prefix: 'leapmux:worker-info:', ttlMs: 7 * DAY_MS },
  { prefix: 'leapmux:local-messages:', ttlMs: 7 * DAY_MS },
]

/** Returns the TTL in ms for a dynamic key, or null if the key is static or unknown. */
export function getTtlForKey(key: string): number | null {
  for (const { prefix, ttlMs } of DYNAMIC_KEY_TTLS) {
    if (key.startsWith(prefix))
      return ttlMs
  }
  return null
}

/** Returns true if the key is a known leapmux key (static or dynamic). */
export function isKnownKey(key: string): boolean {
  if (STATIC_KEYS.has(key))
    return true
  return getTtlForKey(key) !== null
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
 * Returns true if 3+ hours have passed since the expiration was last set.
 */
export function shouldRefreshExpiration(e: number, ttlMs: number): boolean {
  return e < Date.now() + ttlMs - REFRESH_THRESHOLD_MS
}

/**
 * Scan localStorage and delete any `leapmux:` or `leapmux-` key that is
 * NOT a known static key AND NOT a valid non-expired wrapped dynamic key.
 */
export function runCleanup(): void {
  const keysToDelete: string[] = []
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i)
    if (!key)
      continue
    if (!key.startsWith('leapmux:') && !key.startsWith('leapmux-'))
      continue

    // Static keys are never cleaned up.
    if (STATIC_KEYS.has(key))
      continue

    // Check if it's a dynamic key with a valid, non-expired wrapped value.
    const ttl = getTtlForKey(key)
    if (ttl !== null) {
      try {
        const raw = localStorage.getItem(key)
        if (raw !== null) {
          const parsed = JSON.parse(raw)
          if (isWrappedValue(parsed) && parsed.e > Date.now()) {
            continue // Valid and not expired — keep it.
          }
        }
      }
      catch { /* parse error → treat as stale */ }
    }

    // Not a static key, not a valid wrapped dynamic key → delete.
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
