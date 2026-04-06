/**
 * Safe localStorage wrappers that handle JSON parsing/stringifying with try-catch.
 *
 * Dynamic keys (per-agent/per-worker) are automatically wrapped as { v, e }
 * with an expiration timestamp. Static keys (preferences) are stored raw.
 *
 * All keys must be registered in storageCleanup.ts (STATIC_KEYS or DYNAMIC_KEY_TTLS).
 * Unrecognized keys will throw an error.
 */

import { getTtlForKey, isWrappedValue, shouldRefreshExpiration, STATIC_KEYS } from './storageCleanup'

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
      `Unknown localStorage key: "${key}". Register it in storageCleanup.ts `
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
