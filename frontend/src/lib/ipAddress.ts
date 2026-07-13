/**
 * IP address parsing that mirrors Go's net.ParseIP.
 *
 * The tunnel sidecar validates bind addresses with `net.ParseIP(v).IsLoopback()`, and
 * the frontend must agree with it in BOTH directions -- accepting what it accepts and
 * rejecting what it rejects -- or the user gets either a refused-but-valid address or
 * a create that fails at the sidecar with a raw parse error.
 */

/**
 * Strips one surrounding `[...]` pair from an IPv6 literal, as typed in a URL.
 *
 * The submitted value is normalized through this too: validating the stripped form
 * while SUBMITTING the raw one let `[::1]` pass the dialog and then fail at the
 * sidecar, whose `net.ParseIP("[::1]")` returns nil — exactly the failed create this
 * validation exists to prevent.
 */
export function normalizeBindAddr(value: string): string {
  const v = value.trim()
  return v.startsWith('[') && v.endsWith(']') ? v.slice(1, -1) : v
}

/** Parses a dotted-quad exactly as Go's net.ParseIP does, or null. */
function parseIPv4(v: string): number[] | null {
  const parts = v.split('.')
  if (parts.length !== 4)
    return null
  const octets: number[] = []
  for (const part of parts) {
    // Go's net.ParseIP requires 1-3 digits and rejects leading zeros, so
    // "127.0.0.01" and "0177.0.0.1" are NOT addresses to it. Accepting them here
    // would hand the user a create that fails at the sidecar with a raw parse error.
    if (!/^\d{1,3}$/.test(part) || (part.length > 1 && part.startsWith('0')))
      return null
    const n = Number(part)
    if (n > 255)
      return null
    octets.push(n)
  }
  return octets
}

/**
 * Parses an IPv6 literal to its 16 bytes, or null. Handles `::` compression and a
 * trailing embedded IPv4 (`::ffff:127.0.0.1`).
 */
function parseIPv6(v: string): number[] | null {
  if (!v.includes(':'))
    return null
  // A stray leading/trailing colon is only legal as part of "::". Without this the
  // group expansion below silently skips the resulting empty group and accepts
  // "::1:", which net.ParseIP rejects.
  if (v.includes(':::'))
    return null
  if (v.startsWith(':') && !v.startsWith('::'))
    return null
  if (v.endsWith(':') && !v.endsWith('::'))
    return null
  const halves = v.split('::')
  if (halves.length > 2)
    return null

  // A trailing dotted-quad contributes the last 4 bytes.
  let tail: number[] = []
  let body = v
  const lastColon = body.lastIndexOf(':')
  const maybeV4 = body.slice(lastColon + 1)
  if (maybeV4.includes('.')) {
    const v4 = parseIPv4(maybeV4)
    if (!v4)
      return null
    tail = v4
    body = body.slice(0, lastColon + 1)
    // Re-split now that the IPv4 tail is removed.
    halves.length = 0
    halves.push(...body.split('::'))
    if (halves.length > 2)
      return null
  }

  const toBytes = (group: string): number[] | null => {
    if (!/^[0-9a-f]{1,4}$/i.test(group))
      return null
    const n = Number.parseInt(group, 16)
    return [(n >> 8) & 0xFF, n & 0xFF]
  }
  const expand = (side: string): number[] | null => {
    const out: number[] = []
    for (const group of side.split(':')) {
      if (group === '')
        continue
      const bytes = toBytes(group)
      if (!bytes)
        return null
      out.push(...bytes)
    }
    return out
  }

  const head = expand(halves[0])
  if (!head)
    return null
  if (halves.length === 1) {
    const full = [...head, ...tail]
    return full.length === 16 ? full : null
  }
  const rest = expand(halves[1])
  if (!rest)
    return null
  // "::" must stand for at least ONE zero group: net.ParseIP rejects a "::" that
  // compresses nothing ("::1:2:3:4:5:6:7:8"), so accepting fill == 0 here would green-
  // light a bind the sidecar then refuses.
  const fill = 16 - head.length - rest.length - tail.length
  if (fill < 1)
    return null
  return [...head, ...Array.from<number>({ length: fill }).fill(0), ...rest, ...tail]
}

/**
 * Whether an address is loopback — the only place a tunnel may listen.
 *
 * Mirrors the sidecar's validateTunnelBindAddr (`net.ParseIP(v).IsLoopback()`).
 * Neither tunnel listener authenticates, so binding anywhere else would expose an
 * open gateway into the worker's network; the sidecar is the enforcement point and
 * this is the message.
 *
 * It must agree with the sidecar in BOTH directions, which a looser regex did not:
 * Go accepts `0:0:0:0:0:0:0:1` and `::ffff:127.0.0.1` (blocking them here refuses a
 * bind the sidecar would allow), and Go REJECTS `127.0.0.01` (accepting it here
 * promises a create that then fails). Hence real parsing rather than a shape test.
 */
export function isLoopbackAddress(v: string): boolean {
  const v4 = parseIPv4(v)
  if (v4)
    return v4[0] === 127 // the whole 127.0.0.0/8 block

  const v6 = parseIPv6(v)
  if (!v6)
    return false
  // IPv4-mapped (::ffff:a.b.c.d) is loopback iff the embedded v4 is.
  const mapped = v6.slice(0, 12).every((b, i) => (i < 10 ? b === 0 : b === 0xFF))
  if (mapped)
    return v6[12] === 127
  // ::1 and every equivalent spelling of it.
  return v6.slice(0, 15).every(b => b === 0) && v6[15] === 1
}
