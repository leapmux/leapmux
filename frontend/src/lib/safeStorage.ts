/** Safe localStorage wrappers that handle JSON parsing/stringifying with try-catch. */

/** Read and parse a JSON value from localStorage. Returns undefined on missing key or parse error. */
export function safeGetJson<T>(key: string): T | undefined {
  try {
    const raw = localStorage.getItem(key)
    if (raw !== null) {
      return JSON.parse(raw) as T
    }
  }
  catch { /* ignore parse errors */ }
  return undefined
}

/** Stringify and write a JSON value to localStorage. Silently ignores write errors. */
export function safeSetJson(key: string, value: unknown): void {
  try {
    localStorage.setItem(key, JSON.stringify(value))
  }
  catch { /* ignore write errors (e.g. quota exceeded) */ }
}

/** Read a raw string from localStorage. Returns null on missing key or access error. */
export function safeGetString(key: string): string | null {
  try {
    return localStorage.getItem(key)
  }
  catch { /* ignore access errors */ }
  return null
}

/** Write a raw string to localStorage. Silently ignores write errors. */
export function safeSetString(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
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
