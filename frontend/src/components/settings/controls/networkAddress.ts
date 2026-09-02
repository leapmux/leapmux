/**
 * The listen-address grammar the Network access panel edits, and the merge rule
 * it previews.
 *
 * The hub's `internal/hub/listenset` owns both and is the enforcement point.
 * This mirrors them so the panel can tell the operator, BEFORE Apply, which of
 * the addresses they typed will fold into another one — without the preview,
 * an address they asked for is simply absent from "Serving now" a second later
 * and reads as one the hub dropped.
 *
 * A mirror nobody checks stops matching silently, so the merge rule is pinned
 * by `testdata/listen_merge_conformance.json`, which both suites replay.
 */
import { LISTEN_ANY_HOST } from '~/generated/contracts/listen'
import { isLoopbackAddress, isValidPort, parseIPv4, parseIPv6 } from '~/lib/ipAddress'

/**
 * The host every "All interfaces" row carries.
 *
 * The hub's own spelling, from the contract both sides generate from:
 * `listenset` canonicalises an empty host and this token to the same address,
 * so a row built here and a row read back from the hub compare equal.
 */
export const ANY_HOST: string = LISTEN_ANY_HOST

/** One editable address in the panel. */
export interface AddressRow {
  /** Stable across re-renders, so `<For>` does not rebuild a row being typed in. */
  id: number
  /** `*` for every interface, else an IP literal (with a zone where it has one). */
  host: string
  /** Kept as TEXT: a half-typed port is not a number, and clearing the field is not 0. */
  port: string
}

/**
 * What an address SPECIFIES, which is what decides whether one covers another.
 *
 * The same five the hub's `listenset.Kind` states, by the same names.
 */
export type AddressKind = 'any' | 'any-ipv4' | 'any-ipv6' | 'ip' | 'host'

/** One parsed listen address. */
export interface ParsedAddress {
  kind: AddressKind
  /** Canonical host: `''` for the family-neutral wildcard. */
  host: string
  port: string
  /** The address bytes, for `ip`, `any-ipv4` and `any-ipv6`. */
  bytes: number[] | null
  /** The interface zone of a link-local IPv6 address, or `''`. */
  zone: string
}

/**
 * Splits a canonical `host:port` into its two parts.
 *
 * ONE definition of "the port of an address", because three places needed it:
 * the row editor, the new-row default and the merge preview. The other two
 * reached for `split(':').pop()`, which reads the last group of an IPv6
 * literal as a port.
 */
export function splitAddress(address: string): { host: string, port: string } {
  const lastColon = address.lastIndexOf(':')
  if (lastColon < 0)
    return { host: address, port: '' }
  const host = address.slice(0, lastColon)
  return {
    // An IPv6 literal arrives bracketed inside an address and unbracketed on
    // its own; the row edits the bare form and `toAddress` brackets it back.
    host: host.startsWith('[') && host.endsWith(']') ? host.slice(1, -1) : host,
    port: address.slice(lastColon + 1),
  }
}

/** The port of a canonical address, or `''` when it carries none. */
export function portOf(address: string): string {
  return splitAddress(address).port
}

let nextRowID = 0

/** Splits a canonical `host:port` back into the two fields the row edits. */
export function rowFromAddress(address: string): AddressRow {
  return { id: nextRowID++, ...splitAddress(address) }
}

/**
 * Builds a row from its two fields.
 *
 * The counter stays in this module so that no caller can hand out an id that
 * a row already holds. Two rows with one id make `<For>` reconcile them as one,
 * and editing either then rewrites the other.
 */
export function newRow(host: string, port: string): AddressRow {
  return { id: nextRowID++, host, port }
}

/**
 * Renders one row as the canonical address the hub stores.
 *
 * The port is TRIMMED, because `isValidPort` accepts a padded one: it trims
 * before it tests, so a field holding " 8080" enables Apply, and sending the
 * padding would reach the hub as a port it refuses with a message about
 * something else.
 */
export function toAddress(row: AddressRow): string {
  const host = row.host.includes(':') ? `[${row.host}]` : row.host
  return `${host}:${row.port.trim()}`
}

/**
 * Reads a canonical `host:port`, classifying the host the way the hub does.
 *
 * The port stays a STRING: this exists for the merge rule, which compares
 * ports for equality and never arithmetic, and the panel's own field is text.
 */
export function parseAddress(address: string): ParsedAddress {
  const { host, port } = splitAddress(address)
  if (host === '' || host === ANY_HOST)
    return { kind: 'any', host: '', port, bytes: null, zone: '' }

  // A ZONE selects an interface rather than naming a different address, and no
  // IP parser accepts one. It is split off and compared separately, and it
  // keeps its CASE: net.InterfaceByName matches exactly, so `%Ethernet` and
  // `%ethernet` are different interfaces.
  const percent = host.indexOf('%')
  const zone = percent < 0 ? '' : host.slice(percent + 1)
  const literal = percent < 0 ? host : host.slice(0, percent)

  const v4 = parseIPv4(literal)
  if (v4)
    return { kind: isUnspecified(v4) ? 'any-ipv4' : 'ip', host: literal, port, bytes: v4, zone }

  const v6 = parseIPv6(literal)
  if (v6) {
    if (isUnspecified(v6))
      return { kind: mappedIPv4(v6) ? 'any-ipv4' : 'any-ipv6', host: literal, port, bytes: v6, zone }
    return { kind: 'ip', host: literal, port, bytes: v6, zone }
  }

  // Only a NAME reaches here, and a name is case-insensitive.
  return { kind: 'host', host: literal.toLowerCase(), port, bytes: null, zone }
}

/** Whether every byte is zero: the unspecified address of its family. */
function isUnspecified(bytes: number[]): boolean {
  return bytes.every(b => b === 0)
}

/**
 * Whether these bytes bind the IPv4 stack: a dotted quad, or the IPv4-mapped
 * form `::ffff:a.b.c.d`. A mapped address binds IPv4, so the two answer the
 * same question everywhere in this file.
 */
function mappedIPv4(bytes: number[]): boolean {
  if (bytes.length === 4)
    return true
  return bytes.slice(0, 12).every((b, i) => (i < 10 ? b === 0 : b === 0xFF))
}

/** Whether these bytes are IPv4 or an IPv4-mapped IPv6 address. */
function isV4(a: ParsedAddress): boolean {
  return a.bytes !== null && mappedIPv4(a.bytes)
}

/**
 * Whether two parsed addresses are the same address.
 *
 * The bytes are compared AS PARSED, never unwrapped. `::ffff:127.0.0.1` and
 * `127.0.0.1` bind the same stack -- which is why isV4 folds them for the
 * IPv4 wildcard -- but they are two canonical spellings, and the hub keys its
 * live listeners on the canonical string, so it treats them as two addresses
 * here. The differing byte lengths say so on their own.
 */
function sameAddress(a: ParsedAddress, b: ParsedAddress): boolean {
  if (a.kind !== b.kind || a.zone !== b.zone)
    return false
  if (a.bytes === null || b.bytes === null)
    return a.host === b.host
  return a.bytes.length === b.bytes.length && a.bytes.every((byte, i) => byte === b.bytes![i])
}

/**
 * Whether binding `a` makes binding `b` both redundant and impossible: they
 * share a port, and `a` already answers on every address `b` would.
 *
 * This is the whole merge rule, and it is the browser's copy of
 * `listenset.Addr.Covers`. `testdata/listen_merge_conformance.json` is what
 * keeps the two from drifting: both suites replay it.
 *
 * It is deliberately INCOMPLETE in one place, exactly as the hub's is. On a
 * host with `net.ipv6.bindv6only=0`, `[::]` takes the IPv4 stack as well, so
 * `[::]:4327` and `192.168.1.24:4327` collide although neither covers the
 * other here. A sysctl is not readable from an address, so neither side folds
 * that pair; the bind fails and the hub reports it.
 */
export function covers(a: ParsedAddress, b: ParsedAddress): boolean {
  if (a.port !== b.port)
    return false
  switch (a.kind) {
    case 'any':
      return true
    case 'any-ipv4':
      if (b.kind === 'any-ipv4')
        return true
      return b.kind === 'ip' && isV4(b)
    case 'any-ipv6':
      if (b.kind === 'any-ipv6')
        return true
      return b.kind === 'ip' && !isV4(b)
    default:
      return sameAddress(a, b)
  }
}

/**
 * Whether a row states an address only this machine can reach.
 *
 * It reads the ADDRESS, through the same predicate the hub applies:
 * `isLoopbackAddress` mirrors Go's `net.IP.IsLoopback`, which is what the hub
 * computes the wire flag with. Matching the row's host against the reported
 * interface list instead answered "exposed" for every loopback address this
 * machine does not hold verbatim -- `127.0.0.5:4327` written by the admin CLI,
 * or `0:0:0:0:0:0:0:1` where the operating system reports `::1` -- and for
 * EVERY row while the status read was in flight or had failed, so the panel
 * demanded a password for a change that exposes nothing.
 *
 * Every wildcard is exposed: it answers on the loopback interface AND on every
 * other one. A zoned link-local address does not parse here and is exposed
 * too, which is the right answer for one.
 */
export function rowIsLoopback(row: AddressRow): boolean {
  if (row.host === ANY_HOST)
    return false
  return isLoopbackAddress(row.host)
}

/** One address the hub will fold into another one. */
export interface MergeNote {
  /** The address that will not appear on its own. */
  absorbed: string
  /** The address that answers for it. */
  into: string
}

/**
 * Every fold the hub will perform on this list, so the panel can state it
 * BEFORE Apply.
 *
 * The `-listen` address takes part as an ordinary entry, in both directions: a
 * wildcard row absorbs it, and it absorbs a row that it already covers. Only
 * the second half was reported before, and only for a literal `*` row, so an
 * operator who typed `0.0.0.0:4327` beside a `-listen` of `127.0.0.1:4327` saw
 * no note and then one address where they expected two.
 *
 * An entry folds into the FINAL absorber, never an intermediate one. With
 * `0.0.0.0:4327`, `*:4327` and `127.0.0.1:4327` on the list, the third folds
 * into `*:4327` -- which is what the hub binds -- and not into the `0.0.0.0`
 * row that is itself absorbed.
 *
 * Two identical addresses cover each other, so the EARLIER one wins and the
 * later one reads as absorbed. That is the note a duplicated row deserves.
 */
export function mergeNotes(defaultAddress: string, rows: AddressRow[]): MergeNote[] {
  const addresses = [
    ...(defaultAddress === '' ? [] : [defaultAddress]),
    ...rows.map(toAddress),
  ]
  // A half-typed row has no port to compare, and every one of them would then
  // "cover" every other. The panel shows this only for valid rows; the guard
  // keeps that from being a precondition a caller can forget.
  const parsed = addresses.map(a => (isValidPort(portOf(a)) ? parseAddress(a) : null))

  /** For each entry, the entry that absorbs it directly, or -1. */
  const absorber = parsed.map((a, i) => {
    if (a === null)
      return -1
    return parsed.findIndex((b, j) => {
      if (j === i || b === null || !covers(b, a))
        return false
      // Mutual coverage means one address written twice: the earlier entry
      // absorbs the later one. Without the tie-break each would absorb the
      // other and the note would state both folds.
      return j < i || !covers(a, b)
    })
  })

  const notes: MergeNote[] = []
  addresses.forEach((address, i) => {
    let into = absorber[i]
    if (into < 0)
      return
    // Follow the chain to the address the hub actually binds. Coverage is
    // transitive and the tie-break above points every mutual pair backwards,
    // so this cannot cycle; the step limit makes that structural, not a claim.
    for (let step = 0; step < addresses.length && absorber[into] >= 0; step++)
      into = absorber[into]
    notes.push({ absorbed: address, into: addresses[into] })
  })
  return notes
}
