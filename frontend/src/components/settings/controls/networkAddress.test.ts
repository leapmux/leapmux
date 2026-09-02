import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

import { covers, mergeNotes, newRow, parseAddress, portOf, rowFromAddress, rowIsLoopback, splitAddress, toAddress } from './networkAddress'

describe('splitAddress', () => {
  it('splits a canonical host and port', () => {
    expect(splitAddress('127.0.0.1:4327')).toEqual({ host: '127.0.0.1', port: '4327' })
    expect(splitAddress('*:4327')).toEqual({ host: '*', port: '4327' })
  })

  // An IPv6 literal arrives bracketed inside an address and unbracketed on its
  // own; the row edits the bare form and toAddress brackets it back.
  it('unwraps a bracketed IPv6 literal', () => {
    expect(splitAddress('[::1]:4327')).toEqual({ host: '::1', port: '4327' })
    expect(splitAddress('[fe80::1%en0]:4327')).toEqual({ host: 'fe80::1%en0', port: '4327' })
  })

  // `split(':').pop()` reads the last group of an IPv6 literal as a port, which
  // is why this exists as one definition rather than three.
  it('reads an address with no port as all host', () => {
    expect(splitAddress('192.168.1.24')).toEqual({ host: '192.168.1.24', port: '' })
  })

  it('reports the port on its own', () => {
    expect(portOf('[::1]:4327')).toBe('4327')
    expect(portOf('*:4327')).toBe('4327')
    expect(portOf('nonsense')).toBe('')
  })
})

describe('toAddress', () => {
  it('brackets a host that carries a colon, and nothing else', () => {
    expect(toAddress({ id: 0, host: '::1', port: '4327' })).toBe('[::1]:4327')
    expect(toAddress({ id: 0, host: 'fe80::1%en0', port: '4327' })).toBe('[fe80::1%en0]:4327')
    expect(toAddress({ id: 0, host: '127.0.0.1', port: '4327' })).toBe('127.0.0.1:4327')
    expect(toAddress({ id: 0, host: '*', port: '4327' })).toBe('*:4327')
  })

  // isValidPort trims before it tests, so a padded field enables Apply; sending
  // the padding would reach the hub as a port it refuses for another reason.
  it('trims a padded port', () => {
    expect(toAddress({ id: 0, host: '127.0.0.1', port: ' 4327 ' })).toBe('127.0.0.1:4327')
  })

  it('round-trips what rowFromAddress read', () => {
    for (const address of ['*:4327', '127.0.0.1:8080', '[::1]:4327', '[fe80::1%en0]:9000'])
      expect(toAddress(rowFromAddress(address))).toBe(address)
  })

  it('gives each row a distinct id, so <For> does not rebuild one being typed in', () => {
    expect(rowFromAddress('*:1').id).not.toBe(rowFromAddress('*:1').id)
  })
})

describe('rowIsLoopback', () => {
  it('reads the address rather than a list of the machine\'s interfaces', () => {
    expect(rowIsLoopback({ id: 0, host: '127.0.0.1', port: '4327' })).toBe(true)
    // The whole 127.0.0.0/8 block, which this machine does not hold verbatim.
    expect(rowIsLoopback({ id: 0, host: '127.0.0.5', port: '4327' })).toBe(true)
    // Every spelling of ::1, not only the one the operating system reports.
    expect(rowIsLoopback({ id: 0, host: '0:0:0:0:0:0:0:1', port: '4327' })).toBe(true)
    expect(rowIsLoopback({ id: 0, host: '192.168.1.24', port: '4327' })).toBe(false)
  })

  // A wildcard answers on the loopback interface AND on every other one, so
  // calling it loopback would report an exposed hub as private.
  it('calls every wildcard exposed', () => {
    expect(rowIsLoopback({ id: 0, host: '*', port: '4327' })).toBe(false)
    expect(rowIsLoopback({ id: 0, host: '0.0.0.0', port: '4327' })).toBe(false)
    expect(rowIsLoopback({ id: 0, host: '::', port: '4327' })).toBe(false)
  })

  // A zoned link-local address is not loopback, and it does not parse here
  // either -- both roads reach the same, correct answer.
  it('calls a zoned link-local address exposed', () => {
    expect(rowIsLoopback({ id: 0, host: 'fe80::1%en0', port: '4327' })).toBe(false)
  })
})

describe('parseAddress', () => {
  it('classifies each host the way the hub does', () => {
    expect(parseAddress('*:4327').kind).toBe('any')
    expect(parseAddress(':4327').kind).toBe('any')
    expect(parseAddress('0.0.0.0:4327').kind).toBe('any-ipv4')
    expect(parseAddress('[::]:4327').kind).toBe('any-ipv6')
    expect(parseAddress('[0:0:0:0:0:0:0:0]:4327').kind).toBe('any-ipv6')
    expect(parseAddress('127.0.0.1:4327').kind).toBe('ip')
    expect(parseAddress('[::1]:4327').kind).toBe('ip')
    expect(parseAddress('hub.example:4327').kind).toBe('host')
  })

  // A zone selects an interface rather than naming a different address, and
  // net.InterfaceByName matches exactly -- so the zone keeps its case while the
  // hexadecimal digits fold.
  it('keeps an interface zone, with its case', () => {
    const a = parseAddress('[FE80::1%Ethernet]:4327')
    expect(a.kind).toBe('ip')
    expect(a.zone).toBe('Ethernet')
  })

  it('folds a name to lower case, because a name is case-insensitive', () => {
    expect(parseAddress('HUB.EXAMPLE:4327').host).toBe('hub.example')
  })
})

/**
 * The merge rule, replayed from the corpus the hub's own suite replays.
 *
 * `backend/internal/hub/listenset/listenset_test.go` holds the other half.
 * Editing one side without the other is the exact bug the corpus prevents: the
 * language that drifts is the one whose CI turns red.
 */
describe('covers (testdata/listen_merge_conformance.json)', () => {
  const corpus = JSON.parse(
    readFileSync(join(import.meta.dirname, '../../../../../testdata/listen_merge_conformance.json'), 'utf8'),
  ) as { cases: { a: string, b: string, covers: boolean, why: string }[] }

  it('reads a corpus with cases in it', () => {
    expect(corpus.cases.length).toBeGreaterThan(20)
  })

  it.each(corpus.cases)('$a covers $b is $covers ($why)', ({ a, b, covers: want }) => {
    expect(covers(parseAddress(a), parseAddress(b))).toBe(want)
  })
})

describe('mergeNotes', () => {
  const rows = (...addresses: string[]) => addresses.map(rowFromAddress)
  const notes = (defaultAddress: string, ...addresses: string[]) =>
    mergeNotes(defaultAddress, rows(...addresses)).map(n => `${n.absorbed} -> ${n.into}`)

  it('says nothing while every address stands on its own', () => {
    expect(notes('127.0.0.1:4327', '192.168.1.24:4327', '[::1]:9000')).toEqual([])
  })

  it('folds the -listen address into a wildcard row that covers it', () => {
    expect(notes('127.0.0.1:4327', '*:4327')).toEqual(['127.0.0.1:4327 -> *:4327'])
  })

  // The direction the panel never reported: -listen is an ordinary entry, and a
  // wildcard there absorbs the row. The operator saw no note, then one address
  // where they expected two.
  it('folds a row into the -listen address that covers it', () => {
    expect(notes('*:4327', '127.0.0.1:4327')).toEqual(['127.0.0.1:4327 -> *:4327'])
  })

  // The other half the `host === '*'` test missed: 0.0.0.0 is a wildcard the
  // interface menu can produce, and it covers every IPv4 address.
  it('folds into a wildcard the panel does not spell with a star', () => {
    expect(notes('127.0.0.1:4327', '0.0.0.0:4327')).toEqual(['127.0.0.1:4327 -> 0.0.0.0:4327'])
    expect(notes('[::1]:4327', '[::]:4327')).toEqual(['[::1]:4327 -> [::]:4327'])
  })

  it('leaves a different port alone, because it is a different socket', () => {
    expect(notes('127.0.0.1:4327', '*:8080')).toEqual([])
  })

  it('folds every covered address, not only the first', () => {
    expect(notes('127.0.0.1:4327', '*:4327', '192.168.1.24:4327', '[::1]:4327')).toEqual([
      '127.0.0.1:4327 -> *:4327',
      '192.168.1.24:4327 -> *:4327',
      '[::1]:4327 -> *:4327',
    ])
  })

  // The chain: the third address folds into what the hub BINDS, not into the
  // 0.0.0.0 row that is itself absorbed.
  it('names the final absorber, never an intermediate one', () => {
    expect(notes('', '0.0.0.0:4327', '*:4327', '127.0.0.1:4327')).toEqual([
      '0.0.0.0:4327 -> *:4327',
      '127.0.0.1:4327 -> *:4327',
    ])
  })

  it('reports a duplicated row as absorbed by the earlier one', () => {
    expect(notes('', '127.0.0.1:4327', '127.0.0.1:4327')).toEqual(['127.0.0.1:4327 -> 127.0.0.1:4327'])
  })

  it('works with no -listen address to report', () => {
    expect(notes('', '*:4327', '127.0.0.1:4327')).toEqual(['127.0.0.1:4327 -> *:4327'])
  })

  // Two half-typed rows both carry no port. Comparing those ports for equality
  // would make each cover the other, and the panel would promise a fold between
  // two addresses that do not exist yet.
  it('ignores a row whose port is not a port', () => {
    expect(mergeNotes('127.0.0.1:4327', [newRow('*', ''), newRow('*', '99999')])).toEqual([])
  })
})
