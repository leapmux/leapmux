import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import {
  MIN_REKEY_INTERVAL_MS,
  PING_METHOD,
  SESSION_KEY_HARD_CEILING_MS,
  SESSION_KEY_MAX_AGE_MS,
} from './channel'
import {
  DEFAULT_MAX_MESSAGE_SIZE,
  DEFAULT_MAX_REASSEMBLED_MESSAGE_SIZE,
  INNER_ENVELOPE_HEADROOM,
  MAX_CHUNK_SIZE,
  MAX_CONFIGURABLE_MESSAGE_SIZE,
  MAX_INCOMPLETE_CHUNKED,
  maxReassembledMessageSize,
} from './reassembler'

describe('channel-wire protocol limits', () => {
  // The Go implementation (backend/channelwire/wire.go) asserts the SAME fixture
  // (backend/channelwire/wire_test.go). Both ends chunk and reassemble the same
  // encrypted channel messages, so a retune on one side that is not mirrored on the
  // other would silently reject or mis-split a legitimate message at the un-updated
  // receiver; tying both constant sets to one fixture turns that drift into a red
  // build. Resolved from this file, like ipAddress.test.ts, since the fixture lives
  // at the repo root outside vite's root.
  const limits = JSON.parse(
    readFileSync(
      resolve(dirname(fileURLToPath(import.meta.url)), '../../../testdata/channelwire_limits.json'),
      'utf-8',
    ),
  ) as {
    maxPlaintextPerChunk: number
    maxMessageSize: number
    innerEnvelopeHeadroom: number
    maxReassembledMessageSize: number
    maxConfigurableMessageSize: number
    maxIncompleteChunked: number
    pingMethod: string
    sessionKeyMaxAgeMs: number
    minRekeyIntervalMs: number
    sessionKeyHardCeilingMs: number
  }

  it('match the cross-language fixture the Go side also asserts', () => {
    expect(MAX_CHUNK_SIZE).toBe(limits.maxPlaintextPerChunk)
    expect(DEFAULT_MAX_MESSAGE_SIZE).toBe(limits.maxMessageSize)
    expect(INNER_ENVELOPE_HEADROOM).toBe(limits.innerEnvelopeHeadroom)
    expect(DEFAULT_MAX_REASSEMBLED_MESSAGE_SIZE).toBe(limits.maxReassembledMessageSize)
    expect(maxReassembledMessageSize(DEFAULT_MAX_MESSAGE_SIZE)).toBe(DEFAULT_MAX_REASSEMBLED_MESSAGE_SIZE)
    expect(MAX_CONFIGURABLE_MESSAGE_SIZE).toBe(limits.maxConfigurableMessageSize)
    expect(MAX_INCOMPLETE_CHUNKED).toBe(limits.maxIncompleteChunked)
    expect(PING_METHOD).toBe(limits.pingMethod)
    expect(SESSION_KEY_MAX_AGE_MS).toBe(limits.sessionKeyMaxAgeMs)
    expect(MIN_REKEY_INTERVAL_MS).toBe(limits.minRekeyIntervalMs)
    expect(SESSION_KEY_HARD_CEILING_MS).toBe(limits.sessionKeyHardCeilingMs)
  })
})
