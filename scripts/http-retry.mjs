// The HTTP vocabulary that the retrying clients of the build scripts share:
// which statuses are transient, and how long a Retry-After header asks a
// client to wait. scripts/download.mjs and scripts/renovate-registry.mjs
// apply their own retry policies on top of it.

/** The statuses that an overload or an outage answers, which end by themselves. */
export const TRANSIENT_STATUSES = new Set([429, 500, 502, 503, 504])

const DELTA_SECONDS_PATTERN = /^\d+$/
// The IMF-fixdate of RFC 7231. The lenient Date.parse fallback accepts
// shapes that no server sends and no reader may honor, so the date branch
// matches the grammar first.
const HTTP_DATE_PATTERN = /^[A-Z][a-z]{2}, \d{2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}:\d{2} GMT$/

/**
 * The wait that a Retry-After header asks for, in milliseconds: delta
 * seconds or an HTTP date. Null when the header is absent or malformed, so
 * the caller falls back to its backoff.
 */
export function parseRetryAfter(header, now = Date.now()) {
  if (!header)
    return null
  const value = header.trim()
  if (DELTA_SECONDS_PATTERN.test(value))
    return Number(value) * 1000
  if (!HTTP_DATE_PATTERN.test(value))
    return null
  const date = Date.parse(value)
  if (Number.isNaN(date))
    return null
  return Math.max(0, date - now)
}
