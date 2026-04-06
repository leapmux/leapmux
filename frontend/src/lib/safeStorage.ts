/**
 * Safe localStorage wrappers that handle JSON parsing/stringifying with try-catch.
 *
 * Dynamic keys (per-agent/per-worker) are automatically wrapped as { v, e }
 * with an expiration timestamp. Static keys (preferences) are stored raw.
 *
 * All keys must be registered in storageCleanup.ts (STATIC_KEYS or DYNAMIC_KEY_TTLS).
 * Unrecognized keys will throw an error.
 */

import { getTtlForKey, isKnownKey, isWrappedValue, shouldRefreshExpiration } from './storageCleanup'

function assertKnownKey(key: string): void {
  if (!isKnownKey(key)) {
    throw new Error(
      `Unknown localStorage key: "${key}". Register it in storageCleanup.ts `
      + `(STATIC_KEYS or DYNAMIC_KEY_TTLS).`,
    )
  }
}

/** Read and parse a JSON value from localStorage. Returns undefined on missing key or parse error. */
export function safeGetJson<T>(key: string): T | undefined {
  assertKnownKey(key)
  try {
    const raw = localStorage.getItem(key)
    if (raw === null)
      return undefined

    const parsed = JSON.parse(raw)
    const ttl = getTtlForKey(key)

    if (ttl !== null) {
      // Dynamic key — expect wrapped format.
      if (!isWrappedValue(parsed))
        return undefined // Old/unwrapped entry — treat as non-existent.

      if (parsed.e <= Date.now()) {
        // Expired — delete and return undefined.
        localStorage.removeItem(key)
        return undefined
      }

      // Refresh expiration if 3+ hours have passed since last touch.
      if (shouldRefreshExpiration(parsed.e, ttl)) {
        parsed.e = Date.now() + ttl
        localStorage.setItem(key, JSON.stringify(parsed))
      }

      return parsed.v as T
    }

    // Static key — return raw parsed value.
    return parsed as T
  }
  catch { /* ignore parse errors */ }
  return undefined
}

/** Stringify and write a JSON value to localStorage. Silently ignores write errors. */
export function safeSetJson(key: string, value: unknown): void {
  assertKnownKey(key)
  try {
    const ttl = getTtlForKey(key)
    if (ttl !== null) {
      // Dynamic key — wrap with expiration.
      localStorage.setItem(key, JSON.stringify({ v: value, e: Date.now() + ttl }))
    }
    else {
      // Static key — store raw.
      localStorage.setItem(key, JSON.stringify(value))
    }
  }
  catch { /* ignore write errors (e.g. quota exceeded) */ }
}

/** Read a raw string from localStorage. Returns null on missing key or access error. */
export function safeGetString(key: string): string | null {
  assertKnownKey(key)
  try {
    const ttl = getTtlForKey(key)

    if (ttl !== null) {
      // Dynamic key — stored as wrapped JSON.
      const raw = localStorage.getItem(key)
      if (raw === null)
        return null

      const parsed = JSON.parse(raw)
      if (!isWrappedValue(parsed))
        return null // Old/unwrapped entry.

      if (parsed.e <= Date.now()) {
        localStorage.removeItem(key)
        return null
      }

      if (shouldRefreshExpiration(parsed.e, ttl)) {
        parsed.e = Date.now() + ttl
        localStorage.setItem(key, JSON.stringify(parsed))
      }

      return String(parsed.v)
    }

    // Static key — return raw value.
    return localStorage.getItem(key)
  }
  catch { /* ignore access errors */ }
  return null
}

/** Write a raw string to localStorage. Silently ignores write errors. */
export function safeSetString(key: string, value: string): void {
  assertKnownKey(key)
  try {
    const ttl = getTtlForKey(key)
    if (ttl !== null) {
      // Dynamic key — wrap as JSON with expiration.
      localStorage.setItem(key, JSON.stringify({ v: value, e: Date.now() + ttl }))
    }
    else {
      // Static key — store raw string.
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
