import { bytesToHex } from '@noble/hashes/utils.js'

/**
 * Returns a function that yields strings of the form `${prefix}-${timestamp}-${counter}`.
 * The counter disambiguates ids minted within the same millisecond (where
 * `Date.now()` returns the same value); the timestamp keeps ids roughly
 * sortable.
 */
export function makeIdGenerator(prefix: string): () => string {
  let counter = 0
  return () => {
    counter++
    return `${prefix}-${Date.now()}-${counter}`
  }
}

/**
 * RFC 4122 v4 UUID. Prefers `crypto.randomUUID` when available and falls back
 * to a `crypto.getRandomValues`-based implementation otherwise.
 *
 * The fallback exists because `crypto.randomUUID` is gated to "secure
 * contexts": HTTPS pages, plus `localhost`/`127.0.0.1`/`::1`/`file://`. When
 * the solo Hub is reached over plain HTTP from a non-localhost address (e.g.
 * a Tailscale or LAN IP via `-listen 0.0.0.0:4327`), the page is not a secure
 * context and `crypto.randomUUID` is `undefined`. `crypto.getRandomValues`
 * has no such gate.
 */
export function randomUUID(): string {
  if (typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  const b = new Uint8Array(16)
  crypto.getRandomValues(b)
  b[6] = (b[6] & 0x0F) | 0x40 // version 4
  b[8] = (b[8] & 0x3F) | 0x80 // RFC 4122 variant
  const hex = bytesToHex(b)
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
