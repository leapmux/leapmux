import type { MessageInitShape } from '@bufbuild/protobuf'
import type { SessionChannel } from './channelSession'
import type { Session } from './noise'
import type { LoggerCapture } from '~/test-support/loggerCapture'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MAX_INCOMPLETE_CHUNKED } from '~/generated/contracts/wire'
import {
  ChannelMessageFlags,
  InnerMessageSchema,
  InnerRpcResponseSchema,
  InnerStreamMessageSchema,
  RekeyAckSchema,
  RekeyRejectSchema,
  RekeyRequestSchema,
} from '~/generated/proto/leapmux/v1/channel_pb'
import { createTestSession } from '~/test-support/channelSessionDoubles'
import { makeIdentitySession } from '~/test-support/identitySession'
import { captureLoggerOutput } from '~/test-support/loggerCapture'
import { ChannelError } from './channelError'
import { ChannelSession, createSessionChannel, FIRST_REKEY_CORRELATION_ID } from './channelSession'
import { ChannelSessionInbound } from './channelSessionInbound'
import { generateRekeyEphemeral } from './noise-hybrid'
import { Reassembler } from './reassembler'

/** A session record over an identity session, which uses each plaintext as its ciphertext. */
function makeChannel(overrides?: Partial<SessionChannel>): SessionChannel {
  return {
    ...createSessionChannel({
      channelId: 'ch-1',
      session: makeIdentitySession(),
      maxReassembledMessageSize: 1024,
      workerMlkemPub: new Uint8Array(0),
    }),
    ...overrides,
  }
}

/** A ChannelSessionInbound whose deps record each delivery, each reassembly failure and each session end. */
function makeInbound() {
  const delivered: Array<{ correlationId: number, plaintext: Uint8Array }> = []
  const failed: Array<{ correlationId: number, message: string }> = []
  const ended: ChannelError[] = []
  const session = new ChannelSession({
    postFrame: () => {},
    endSession: (ch, error) => endSession(ch, error),
  })
  function endSession(ch: SessionChannel, error: ChannelError): void {
    if (ch.ended)
      return
    session.dispose(ch)
    ended.push(error)
  }
  const inbound = new ChannelSessionInbound({
    session,
    postDeliver: (_ch, correlationId, plaintext) => delivered.push({ correlationId, plaintext }),
    postReassemblyFailed: (_ch, correlationId, message) => failed.push({ correlationId, message }),
    endSession,
  })
  return { inbound, delivered, failed, ended }
}

function envelopeBytes(envelope: MessageInitShape<typeof InnerMessageSchema>): Uint8Array {
  return toBinary(InnerMessageSchema, create(InnerMessageSchema, envelope))
}

function responsePlaintext(): Uint8Array {
  return envelopeBytes({
    kind: {
      case: 'response',
      value: create(InnerRpcResponseSchema, {
        result: {
          case: 'payload',
          value: new Uint8Array([7]),
        },
      }),
    },
  })
}

function streamPlaintext(): Uint8Array {
  return envelopeBytes({
    kind: {
      case: 'stream',
      value: create(InnerStreamMessageSchema, {
        payload: new Uint8Array([8]),
      }),
    },
  })
}

/** A session that refuses to decrypt anything. */
function failingSession(): Session {
  const session = makeIdentitySession()
  return {
    send: session.send,
    receive: {
      ...session.receive,
      decrypt: () => {
        throw new Error('bad tag')
      },
    },
  }
}

describe('ChannelSessionInbound handleInbound', () => {
  it('ends the session when decrypt fails', () => {
    const ch = makeChannel({
      session: failingSession(),
    })
    const { inbound, ended, delivered } = makeInbound()

    inbound.handleInbound(ch, 1n, ChannelMessageFlags.UNSPECIFIED, new Uint8Array([1]))

    expect(ended).toHaveLength(1)
    expect(ended[0]).toBeInstanceOf(ChannelError)
    expect(ended[0]).toMatchObject({ source: 'transport', message: 'decrypt a channel message: bad tag', disconnected: false })
    expect(delivered).toEqual([])
    expect(ch.ended).toBe(true)
  })

  it('does nothing for a session that ended', () => {
    const decrypt = vi.fn((ct: Uint8Array) => ct)
    const identity = makeIdentitySession()
    const ch = makeChannel({
      session: {
        send: identity.send,
        receive: { ...identity.receive, decrypt },
      },
    })
    ch.ended = true
    const { inbound } = makeInbound()

    inbound.handleInbound(ch, 1n, ChannelMessageFlags.UNSPECIFIED, responsePlaintext())

    expect(decrypt).not.toHaveBeenCalled()
  })

  // Noise nonces are implicit, so a dropped frame must still be decrypted, or
  // every later frame fails to decrypt.
  it('drops an out-of-range correlation id without ending the session, and the next frame still decrypts', () => {
    const pair = createTestSession()
    const ch = makeChannel({ session: pair.initiator })
    ch.liveIds.add(4)
    ch.highestStartedId = 4
    const { inbound, delivered, ended } = makeInbound()

    inbound.handleInbound(ch, BigInt(Number.MAX_SAFE_INTEGER) + 1n, ChannelMessageFlags.UNSPECIFIED, pair.responder.send.encrypt(responsePlaintext()))
    inbound.handleInbound(ch, 4n, ChannelMessageFlags.UNSPECIFIED, pair.responder.send.encrypt(responsePlaintext()))

    expect(ended).toEqual([])
    expect(delivered.map(entry => entry.correlationId)).toEqual([4])
  })

  it('drops out-of-spec flags after the decrypt, and the next frame still decrypts', () => {
    const pair = createTestSession()
    const ch = makeChannel({ session: pair.initiator })
    ch.liveIds.add(4)
    ch.highestStartedId = 4
    const { inbound, delivered, ended } = makeInbound()

    inbound.handleInbound(ch, 4n, ChannelMessageFlags.MORE | ChannelMessageFlags.CLOSE, pair.responder.send.encrypt(responsePlaintext()))
    inbound.handleInbound(ch, 4n, ChannelMessageFlags.UNSPECIFIED, pair.responder.send.encrypt(responsePlaintext()))

    expect(ended).toEqual([])
    expect(delivered.map(entry => entry.correlationId)).toEqual([4])
  })

  it('routes a rekeyAck through the session with its correlation id', () => {
    const wait = vi.fn()
    // Use a real ephemeral so the fresh-DH derivation in handleRekeyOutcome
    // succeeds (the session itself is an identity session; only the routing is under test).
    const eph = generateRekeyEphemeral()
    const ch = makeChannel({
      rekeyWait: wait,
      rekeyRequestId: FIRST_REKEY_CORRELATION_ID,
      rekeyMaterial: { ePriv: eph.privateKey, mlkemSS: null },
    })
    const { inbound, delivered } = makeInbound()

    inbound.handleInbound(ch, BigInt(FIRST_REKEY_CORRELATION_ID), ChannelMessageFlags.UNSPECIFIED, envelopeBytes({
      kind: {
        case: 'rekeyAck',
        value: create(RekeyAckSchema, { dhPub: eph.publicKey }),
      },
    }))

    expect(wait).toHaveBeenCalledWith(true, 0)
    expect(delivered).toEqual([])
  })

  it('routes a rekeyReject with its retry delay through the session', () => {
    const wait = vi.fn()
    const ch = makeChannel({ rekeyWait: wait, rekeyRequestId: FIRST_REKEY_CORRELATION_ID })
    const { inbound, delivered } = makeInbound()

    inbound.handleInbound(ch, BigInt(FIRST_REKEY_CORRELATION_ID), ChannelMessageFlags.UNSPECIFIED, envelopeBytes({
      kind: {
        case: 'rekeyReject',
        value: create(RekeyRejectSchema, { retryAfterMs: 1500n }),
      },
    }))

    expect(wait).toHaveBeenCalledWith(false, 1500)
    expect(delivered).toEqual([])
  })

  // A delay of 0 lets the session apply its default backoff.
  it('routes a rekeyReject without a retry delay as a delay of 0', () => {
    const wait = vi.fn()
    const ch = makeChannel({ rekeyWait: wait, rekeyRequestId: FIRST_REKEY_CORRELATION_ID })
    const { inbound } = makeInbound()

    inbound.handleInbound(ch, BigInt(FIRST_REKEY_CORRELATION_ID), ChannelMessageFlags.UNSPECIFIED, envelopeBytes({
      kind: {
        case: 'rekeyReject',
        value: create(RekeyRejectSchema, {}),
      },
    }))

    expect(wait).toHaveBeenCalledExactlyOnceWith(false, 0)
  })

  it('posts deliver for a response and for a stream message, with the whole envelope', () => {
    const ch = makeChannel()
    ch.liveIds.add(3)
    ch.liveIds.add(4)
    ch.highestStartedId = 4
    const { inbound, delivered } = makeInbound()

    inbound.handleInbound(ch, 3n, ChannelMessageFlags.UNSPECIFIED, responsePlaintext())
    inbound.handleInbound(ch, 4n, ChannelMessageFlags.UNSPECIFIED, streamPlaintext())

    expect(delivered.map(entry => entry.correlationId)).toEqual([3, 4])
    expect(fromBinary(InnerMessageSchema, delivered[0]!.plaintext).kind.case).toBe('response')
    expect(fromBinary(InnerMessageSchema, delivered[1]!.plaintext).kind.case).toBe('stream')
  })

  it('never posts deliver for a rekey kind, or for an envelope that does not decode', () => {
    const ch = makeChannel({ rekeyRequestId: FIRST_REKEY_CORRELATION_ID })
    ch.liveIds.add(3)
    ch.highestStartedId = 3
    const { inbound, delivered, ended } = makeInbound()
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})

    // A Reject with no rekey in flight, and a RekeyRequest, which a peer never sends.
    inbound.handleInbound(ch, BigInt(FIRST_REKEY_CORRELATION_ID), ChannelMessageFlags.UNSPECIFIED, envelopeBytes({
      kind: {
        case: 'rekeyReject',
        value: create(RekeyRejectSchema, {}),
      },
    }))
    inbound.handleInbound(ch, 3n, ChannelMessageFlags.UNSPECIFIED, envelopeBytes({
      kind: {
        case: 'rekeyRequest',
        value: create(RekeyRequestSchema, {}),
      },
    }))
    // A truncated varint, which no envelope decoder accepts.
    inbound.handleInbound(ch, 3n, ChannelMessageFlags.UNSPECIFIED, Uint8Array.of(0x0A, 0xFF))

    expect(delivered).toEqual([])
    expect(ended).toEqual([])
    expect(errorSpy).toHaveBeenCalledWith('[channel]', 'Failed to deserialize InnerMessage', expect.anything())
    errorSpy.mockRestore()
    warnSpy.mockRestore()
  })

  it('poisons the id and posts reassemblyFailed when two chunks break the size limit', () => {
    const ch = makeChannel({
      maxReassembledMessageSize: 50,
      reassembly: new Reassembler(50),
    })
    ch.liveIds.add(7)
    ch.highestStartedId = 7
    const { inbound, delivered, failed } = makeInbound()
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    inbound.handleInbound(ch, 7n, ChannelMessageFlags.MORE, new Uint8Array(30))
    inbound.handleInbound(ch, 7n, ChannelMessageFlags.MORE, new Uint8Array(30))

    expect(failed).toEqual([
      { correlationId: 7, message: 'chunked message too large: 60 bytes exceeds 50 byte limit' },
    ])
    expect(ch.reassembly.get(7)?.poisoned).toBe(true)

    // The rest of the message is dropped in silence, and its final chunk reaps the tombstone.
    inbound.handleInbound(ch, 7n, ChannelMessageFlags.UNSPECIFIED, new Uint8Array(1))
    expect(failed).toHaveLength(1)
    expect(delivered).toEqual([])
    expect(ch.reassembly.size()).toBe(0)
    errorSpy.mockRestore()
  })

  it('posts "too many incomplete chunked messages" for the sequence past the live cap', () => {
    const ch = makeChannel()
    const { inbound, failed } = makeInbound()
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    for (let id = 1; id <= MAX_INCOMPLETE_CHUNKED + 1; id++)
      ch.liveIds.add(id)
    ch.highestStartedId = MAX_INCOMPLETE_CHUNKED + 1

    for (let id = 1; id <= MAX_INCOMPLETE_CHUNKED + 1; id++)
      inbound.handleInbound(ch, BigInt(id), ChannelMessageFlags.MORE, new Uint8Array(1))

    expect(failed).toEqual([
      { correlationId: MAX_INCOMPLETE_CHUNKED + 1, message: 'too many incomplete chunked messages' },
    ])
    expect(ch.reassembly.liveCount()).toBe(MAX_INCOMPLETE_CHUNKED)
    expect(ch.reassembly.get(MAX_INCOMPLETE_CHUNKED + 1)?.poisoned).toBe(true)
    errorSpy.mockRestore()
  })

  it('reassembles a chunked response for a live id', () => {
    const ch = makeChannel()
    ch.liveIds.add(2)
    ch.highestStartedId = 2
    const { inbound, delivered } = makeInbound()
    const whole = responsePlaintext()

    inbound.handleInbound(ch, 2n, ChannelMessageFlags.MORE, whole.slice(0, 3))
    inbound.handleInbound(ch, 2n, ChannelMessageFlags.UNSPECIFIED, whole.slice(3))

    expect(delivered).toHaveLength(1)
    expect([...delivered[0]!.plaintext]).toEqual([...whole])
  })
})

const RETIRED_ID_MESSAGE = 'dropped a late frame for a retired correlation id'
const NEVER_ALLOCATED_ID_MESSAGE = 'dropped a frame for a correlation id that this client never allocated'

// A frame whose correlation id has no consumer is dropped either way. What these
// pin is the LEVEL: a late frame for an id this client retired on purpose (an
// aborted or timed-out call, a cancelled stream's end frame) is expected traffic,
// while an id this client never allocated is a protocol violation.
describe('ChannelSessionInbound frames with no consumer', () => {
  let capture: LoggerCapture
  beforeEach(() => {
    capture = captureLoggerOutput()
  })
  afterEach(() => capture.restore())

  // Ids 1..4 started requests that have since retired: none is live.
  function deliver(correlationId: number, flags = ChannelMessageFlags.UNSPECIFIED, overrides?: Partial<SessionChannel>) {
    const ch = makeChannel({ highestStartedId: 4, ...overrides })
    const { inbound, delivered } = makeInbound()
    inbound.handleInbound(ch, BigInt(correlationId), flags, responsePlaintext())
    expect(delivered).toEqual([])
    return ch
  }

  it('logs a frame for a retired correlation id at debug level instead of warning', () => {
    deliver(4)
    expect(capture.messages('warn', 'channel')).toEqual([])
    expect(capture.messages('debug', 'channel')).toContain(RETIRED_ID_MESSAGE)
  })

  it('treats the first id that a channel allocates as retired', () => {
    deliver(1)
    expect(capture.messages('warn', 'channel')).toEqual([])
    expect(capture.messages('debug', 'channel')).toContain(RETIRED_ID_MESSAGE)
  })

  it('logs a MORE chunk for a retired correlation id at debug level and buffers nothing', () => {
    const ch = deliver(3, ChannelMessageFlags.MORE)
    expect(capture.messages('warn', 'channel')).toEqual([])
    expect(capture.messages('debug', 'channel')).toContain(RETIRED_ID_MESSAGE)
    expect(ch.reassembly.size()).toBe(0)
  })

  it('treats a RekeyRequest id that the session sent as retired', () => {
    deliver(FIRST_REKEY_CORRELATION_ID + 1, ChannelMessageFlags.UNSPECIFIED, { nextRekeyId: FIRST_REKEY_CORRELATION_ID + 2 })
    expect(capture.messages('warn', 'channel')).toEqual([])
    expect(capture.messages('debug', 'channel')).toContain(RETIRED_ID_MESSAGE)
  })

  it('warns for a RekeyRequest id that the session has not sent', () => {
    deliver(FIRST_REKEY_CORRELATION_ID + 2, ChannelMessageFlags.UNSPECIFIED, { nextRekeyId: FIRST_REKEY_CORRELATION_ID + 2 })
    expect(capture.messages('warn', 'channel')).toEqual([NEVER_ALLOCATED_ID_MESSAGE])
  })

  it('warns for an id above the highest one that a request started with', () => {
    deliver(5)
    expect(capture.messages('warn', 'channel')).toEqual([NEVER_ALLOCATED_ID_MESSAGE])
    expect(capture.messages('debug', 'channel')).not.toContain(RETIRED_ID_MESSAGE)
  })

  it('warns for correlation id 0, which no request ever receives', () => {
    deliver(0)
    expect(capture.messages('warn', 'channel')).toEqual([NEVER_ALLOCATED_ID_MESSAGE])
  })

  it('warns for the largest correlation id that the client still routes', () => {
    deliver(Number.MAX_SAFE_INTEGER)
    expect(capture.messages('warn', 'channel')).toEqual([NEVER_ALLOCATED_ID_MESSAGE])
  })

  it('logs nothing about dropping a frame for a live id', () => {
    const ch = makeChannel({ highestStartedId: 4 })
    ch.liveIds.add(4)
    const { inbound, delivered } = makeInbound()
    inbound.handleInbound(ch, 4n, ChannelMessageFlags.UNSPECIFIED, responsePlaintext())
    expect(delivered.map(entry => entry.correlationId)).toEqual([4])
    expect(capture.messages('warn', 'channel')).toEqual([])
    expect(capture.messages('debug', 'channel')).not.toContain(RETIRED_ID_MESSAGE)
  })
})
