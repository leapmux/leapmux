import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { isLoopbackAddress, normalizeBindAddr } from './ipAddress'

/**
 * A case from the cross-language conformance fixture. See that file's `_readme` for the
 * contract; the short version is that the user typed `input`, the dialog normalizes it to
 * `normalized` (the value it both validates AND submits), and the sidecar accepts
 * `normalized` iff `loopback`. This suite asserts the dialog's half of that sentence;
 * desktop/go/tunnel_test.go asserts the sidecar's.
 */
interface ConformanceCase {
  input: string
  normalized: string
  loopback: boolean
  why: string
}

/**
 * Resolved from this file rather than the CWD: vitest's working directory is not part of
 * the contract, and the fixture lives at the repo root (outside `src`, so outside
 * tsconfig's `include` and vite's root -- which is why this is a runtime read rather than
 * a static JSON import).
 */
const conformancePath = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../../testdata/tunnel_bind_addr_conformance.json',
)

describe('normalizeBindAddr', () => {
  it('strips one surrounding bracket pair from an IPv6 literal', () => {
    expect(normalizeBindAddr('[::1]')).toBe('::1')
    expect(normalizeBindAddr('[::ffff:127.0.0.1]')).toBe('::ffff:127.0.0.1')
  })

  it('strips exactly one pair, leaving a nested one intact', () => {
    expect(normalizeBindAddr('[[::1]]')).toBe('[::1]')
  })

  it('trims surrounding whitespace', () => {
    expect(normalizeBindAddr('  127.0.0.1  ')).toBe('127.0.0.1')
    expect(normalizeBindAddr(' [::1] ')).toBe('::1')
  })

  it('leaves an unbracketed or half-bracketed value alone', () => {
    expect(normalizeBindAddr('127.0.0.1')).toBe('127.0.0.1')
    expect(normalizeBindAddr('[::1')).toBe('[::1')
    expect(normalizeBindAddr('::1]')).toBe('::1]')
  })

  it('returns empty for an empty or whitespace-only value', () => {
    expect(normalizeBindAddr('')).toBe('')
    expect(normalizeBindAddr('   ')).toBe('')
  })
})

// isLoopbackAddress must agree with the sidecar's net.ParseIP(v).IsLoopback() in BOTH
// directions. These expectations were taken from running Go itself; a shape-matching
// regex got both wrong -- it blocked binds the sidecar allows, and waved through
// "127.0.0.01" (Number('01') === 1) which the sidecar then rejects with a raw parse
// error, i.e. the exact failed create this validation exists to prevent.
describe('isLoopbackAddress', () => {
  describe('iPv4', () => {
    const accepts = ['127.0.0.1', '127.0.0.2', '127.0.0.0', '127.255.255.254']
    for (const addr of accepts) {
      it(`accepts ${addr}, in the 127.0.0.0/8 block`, () => {
        expect(isLoopbackAddress(addr)).toBe(true)
      })
    }

    const rejects = [
      '0.0.0.0',
      '192.168.1.5',
      '10.0.0.1',
      '128.0.0.1', // just outside 127/8
      '126.255.255.255', // just below 127/8
      '127.0.0.256', // octet out of range
    ]
    for (const addr of rejects) {
      it(`rejects ${addr}, which is not loopback`, () => {
        expect(isLoopbackAddress(addr)).toBe(false)
      })
    }

    // Go's net.ParseIP requires 1-3 digits per octet and rejects leading zeros, so
    // these are not addresses at all to the sidecar.
    const malformed = [
      '127.0.0.01', // leading zero: net.ParseIP returns nil
      '0177.0.0.1', // octal-looking: net.ParseIP returns nil
      '127.000.000.001', // ...same reason
      '127.1', // short form: net.ParseIP returns nil
      '2130706433', // integer form: net.ParseIP returns nil
      '127.0.0.1.1', // too many octets
    ]
    for (const addr of malformed) {
      it(`rejects ${addr}, which net.ParseIP does not parse`, () => {
        expect(isLoopbackAddress(addr)).toBe(false)
      })
    }
  })

  describe('iPv6', () => {
    const accepts = [
      '::1',
      '0:0:0:0:0:0:0:1', // uncompressed ::1
      '0000:0000:0000:0000:0000:0000:0000:0001',
      '0::1',
      '::0:1',
      '::ffff:127.0.0.1', // IPv4-mapped loopback
      '::ffff:127.1.2.3',
      '::ffff:7f00:1', // the same mapped address in hex groups
    ]
    for (const addr of accepts) {
      it(`accepts ${addr}, which the sidecar accepts`, () => {
        expect(isLoopbackAddress(addr)).toBe(true)
      })
    }

    const rejects = [
      '::', // all interfaces, v6
      '1::', // well-formed, but the unspecified-ish 1:: is not loopback
      '::2',
      '::ffff:192.168.1.5', // IPv4-mapped, but NOT loopback
      '::ffff:0.0.0.0', // v4-mapped, but not loopback
      '::127.0.0.1', // v4-COMPATIBLE (not v4-mapped): Go does not call this loopback
      'fe80::1',
    ]
    for (const addr of rejects) {
      it(`rejects ${addr}, which is not loopback`, () => {
        expect(isLoopbackAddress(addr)).toBe(false)
      })
    }

    // Malformed IPv6. A group expansion that silently skips empty groups accepts
    // several of these; net.ParseIP accepts none.
    const malformed = [
      '::1:', // trailing colon is only legal as part of "::"
      ':1',
      '1:',
      '::1::2', // two "::" runs
      ':::1',
      // A "::" must compress at least ONE zero group. These are full-length already, so
      // net.ParseIP rejects them -- found by differentially fuzzing this parser against
      // Go over 40k inputs, which a hand-picked table had missed.
      '::0:0:0:0:0:0:0:1',
      '0:0:0:0:0:0:0:1::',
      '::1:2:3:4:5:6:7:8',
      '1:2:3:4:5:6:7:8:9', // too many groups
      '0:0:0:0:0:0:1', // too few groups, no "::"
      '12345::1', // group out of range
      'fe80::1%eth0', // zone id
      '::ffff:127.0.0.01', // leading zero in the embedded v4
      '[::1]', // brackets are normalizeBindAddr's job, not net.ParseIP's
    ]
    for (const addr of malformed) {
      it(`rejects ${addr}, which net.ParseIP does not parse`, () => {
        expect(isLoopbackAddress(addr)).toBe(false)
      })
    }
  })

  describe('non-addresses', () => {
    const rejects = ['', '   ', 'localhost', 'example.com', 'not-an-ip', 'my-host.local', '::/0', '127.0.0.1/8', '127.0.0.1:80']
    for (const value of rejects) {
      it(`rejects ${JSON.stringify(value)}`, () => {
        expect(isLoopbackAddress(value)).toBe(false)
      })
    }
  })
})

// The TS half of the shared conformance fixture. Its Go twin is
// TestValidateTunnelBindAddr in desktop/go/tunnel_test.go; both read the same file, so a
// rule change on either side that is not mirrored turns THIS suite (or that one) red --
// which is the entire reason the fixture exists. Add cases to the fixture, not here.
describe('tunnel bind-address conformance fixture', () => {
  const cases: ConformanceCase[] = JSON.parse(readFileSync(conformancePath, 'utf8')).cases

  it('loads the shared fixture', () => {
    // Guards the classic failure: a fixture that silently resolves to zero cases turns a
    // conformance suite into a no-op that still reports green.
    expect(cases.length).toBeGreaterThan(0)
  })

  for (const c of cases) {
    it(`${JSON.stringify(c.input)} -> ${JSON.stringify(c.normalized)}, loopback=${c.loopback} (${c.why})`, () => {
      expect(normalizeBindAddr(c.input)).toBe(c.normalized)
      expect(isLoopbackAddress(c.normalized)).toBe(c.loopback)
    })
  }
})
