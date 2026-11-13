import type { MessageInitShape } from '@bufbuild/protobuf'
import type { ChannelSessionEndpoint, ChannelSessionEndpointOptions } from './channelSessionEndpoint'
import type { SessionEvent } from './channelSessionProtocol'
import type { CipherStateLike, Session } from './noise'
import type { HybridHandshakeState } from './noise-hybrid'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MAX_CHUNK_SIZE, SESSION_KEY_HARD_CEILING_MS, SESSION_KEY_MAX_AGE_MS, SESSION_VERIFY_TIMEOUT_MS } from '~/generated/contracts/wire'
import {
  ChannelMessageFlags,
  ChannelMessageSchema,
  EncryptionMode,
  InnerMessageSchema,
  InnerRpcResponseSchema,
  RekeyAckSchema,
  RekeyRejectSchema,
} from '~/generated/proto/leapmux/v1/channel_pb'
import { blankClassicHandshakeState, blankHybridHandshakeState, createTestSession } from '~/test-support/channelSessionDoubles'
import { unframeBytes } from './channelFraming'
import { FIRST_REKEY_CORRELATION_ID } from './channelSession'
import { createChannelSessionEndpoint } from './channelSessionEndpoint'
import { isDebugEnabled, setDebugEnabled } from './logger'
import { generateRekeyEphemeral } from './noise-hybrid'
import { yieldToEventLoop } from './workSlices'

const keys = {
  x25519PublicKey: new Uint8Array(32),
  mlkemPublicKey: new Uint8Array(1568),
  slhdsaPublicKey: new Uint8Array(64),
}

/** A posted event and its transfer list. */
interface Posted {
  event: SessionEvent
  transfer: Transferable[]
}

/** Every endpoint that a test built, disposed after the test so that no rekey timer outlives it. */
const endpoints: ChannelSessionEndpoint[] = []

/**
 * An endpoint whose `post` records each event with its transfer list. The mock
 * handshake 1 returns a fresh blank state each time and records it in `states`,
 * and handshake 2 returns the initiator of a TestCipherState pair, whose
 * responder the test plays.
 */
function endpointHarness(options: ChannelSessionEndpointOptions = {}) {
  const posted: Posted[] = []
  const closeSelf = vi.fn()
  const pair = createTestSession()
  const states: HybridHandshakeState[] = []
  const endpoint = createChannelSessionEndpoint({
    post: (event, transfer) => posted.push({ event, transfer }),
    closeSelf,
  }, {
    handshake1: () => {
      const handshakeState = blankHybridHandshakeState()
      states.push(handshakeState)
      return {
        message1: new Uint8Array(1616),
        handshakeState,
      }
    },
    handshake2: async () => pair.initiator,
    idleRekeyIntervalMs: 0,
    ...options,
  })
  endpoints.push(endpoint)
  return {
    endpoint,
    posted,
    closeSelf,
    pair,
    states,
  }
}

type Harness = ReturnType<typeof endpointHarness>

/** The events of `type`, in post order. */
function eventsOf<T extends SessionEvent['type']>(posted: Posted[], type: T): Array<Extract<SessionEvent, { type: T }>> {
  return posted.map(entry => entry.event).filter((event): event is Extract<SessionEvent, { type: T }> => event.type === type)
}

/** Begin and finish a handshake that registers `channelId`, under request ids from `firstRequestId`. */
async function openSession(h: Harness, channelId = 'ch-1', firstRequestId = 1, maxReassembledMessageSize = 1 << 20): Promise<void> {
  h.endpoint.handleCommand({
    type: 'beginHandshake',
    requestId: firstRequestId,
    mode: EncryptionMode.POST_QUANTUM,
    keys,
  })
  h.endpoint.handleCommand({
    type: 'finishHandshake',
    requestId: firstRequestId + 1,
    handshakeId: firstRequestId,
    channelId,
    payload: new Uint8Array(8),
    maxReassembledMessageSize,
  })
  await yieldToEventLoop()
}

/** The ChannelMessage of a posted wire frame. */
function messageOf(frame: Uint8Array) {
  const unframed = unframeBytes(frame)
  if (!unframed.ok)
    throw new Error(`a frame failed to unframe: ${unframed.failure.kind}`)
  return fromBinary(ChannelMessageSchema, unframed.payload)
}

/** Decrypt a posted wire frame the way the Worker does, in the order of the calls. */
function workerReads(h: Harness, frame: Uint8Array) {
  const message = messageOf(frame)
  return {
    message,
    plaintext: h.pair.responder.receive.decrypt(message.ciphertext),
  }
}

/** Send `envelope` from the Worker under `correlationId`. */
function workerSends(h: Harness, correlationId: number, envelope: MessageInitShape<typeof InnerMessageSchema>, flags = ChannelMessageFlags.UNSPECIFIED, channelId = 'ch-1'): void {
  const ciphertext = h.pair.responder.send.encrypt(toBinary(InnerMessageSchema, create(InnerMessageSchema, envelope)))
  h.endpoint.handleCommand({
    type: 'inbound',
    channelId,
    correlationId: BigInt(correlationId),
    flags,
    ciphertext,
  })
}

/** Send raw chunk bytes from the Worker under `correlationId`. */
function workerSendsChunk(h: Harness, correlationId: number, chunk: Uint8Array, flags: ChannelMessageFlags): void {
  h.endpoint.handleCommand({
    type: 'inbound',
    channelId: 'ch-1',
    correlationId: BigInt(correlationId),
    flags,
    ciphertext: h.pair.responder.send.encrypt(chunk),
  })
}

const response: MessageInitShape<typeof InnerMessageSchema> = {
  kind: {
    case: 'response',
    value: create(InnerRpcResponseSchema, {
      result: {
        case: 'payload',
        value: new Uint8Array([7]),
      },
    }),
  },
}

/** Answer the latest RekeyRequest on the wire with an Ack, the way the Worker does. */
function workerAcksRekey(h: Harness): void {
  const frames = eventsOf(h.posted, 'wire')
  const { message, plaintext } = workerReads(h, frames.at(-1)!.frame)
  expect(fromBinary(InnerMessageSchema, plaintext).kind.case).toBe('rekeyRequest')
  h.pair.responder.receive.rekeyWithSecret(new Uint8Array(32), null, true)
  const ciphertext = h.pair.responder.send.encrypt(toBinary(InnerMessageSchema, create(InnerMessageSchema, {
    kind: {
      case: 'rekeyAck',
      value: create(RekeyAckSchema, { dhPub: generateRekeyEphemeral().publicKey }),
    },
  })))
  h.pair.responder.send.rekeyWithSecret(new Uint8Array(32), null, false)
  h.endpoint.handleCommand({
    type: 'inbound',
    channelId: 'ch-1',
    correlationId: message.correlationId,
    flags: ChannelMessageFlags.UNSPECIFIED,
    ciphertext,
  })
}

/** Age the session of `channelId` past its max age, so a rekey is due. */
function age(h: Harness, channelId = 'ch-1', by = SESSION_KEY_MAX_AGE_MS + 1): void {
  h.endpoint.sessionState(channelId)!.lastRekeyAt = performance.now() - by
}

/** Put non-zero bytes into every secret of a hybrid handshake state. */
function fillSecrets(state: HybridHandshakeState): void {
  state.e.privateKey.fill(1)
  state.ss.ck.fill(2)
  state.ss.k.fill(3)
  state.mlkemSS.fill(4)
}

/** Whether every secret of a hybrid handshake state is zero. */
function secretsZero(state: HybridHandshakeState): boolean {
  return [
    state.e.privateKey,
    state.ss.ck,
    state.ss.k,
    state.mlkemSS,
  ].every(bytes => bytes.every(byte => byte === 0))
}

afterEach(() => {
  for (const endpoint of endpoints.splice(0))
    endpoint.dispose()
  vi.useRealTimers()
})

describe('createChannelSessionEndpoint handshake', () => {
  it('answers beginHandshake with handshakeBegun, and transfers the buffer of message 1', () => {
    const h = endpointHarness()

    h.endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 4,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })

    expect(h.posted).toHaveLength(1)
    const [{ event, transfer }] = h.posted as [Posted]
    expect(event).toMatchObject({ type: 'handshakeBegun', requestId: 4 })
    const message1 = (event as Extract<SessionEvent, { type: 'handshakeBegun' }>).message1
    expect(message1.byteLength).toBe(1616)
    expect(transfer).toEqual([message1.buffer])
  })

  it('answers requestFailed when handshake 1 throws', () => {
    const h = endpointHarness({
      handshake1: () => {
        throw new Error('bad key size')
      },
    })

    h.endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 4,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })

    expect(eventsOf(h.posted, 'requestFailed')).toEqual([
      {
        type: 'requestFailed',
        requestId: 4,
        error: {
          source: 'transport',
          message: 'bad key size',
          code: 0,
          fatal: false,
          disconnected: true,
        },
      },
    ])
  })

  it('zeroes the state, files nothing, and answers requestFailed when handshakeBegun cannot be posted', async () => {
    const posted: SessionEvent[] = []
    const state = blankHybridHandshakeState()
    const endpoint = createChannelSessionEndpoint({
      post: (event) => {
        if (event.type === 'handshakeBegun')
          throw new Error('the message cannot be cloned')
        posted.push(event)
      },
      closeSelf: () => {},
    }, {
      handshake1: () => {
        fillSecrets(state)
        return {
          message1: new Uint8Array(1616),
          handshakeState: state,
        }
      },
      idleRekeyIntervalMs: 0,
    })
    endpoints.push(endpoint)

    endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 4,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })
    endpoint.handleCommand({
      type: 'finishHandshake',
      requestId: 5,
      handshakeId: 4,
      channelId: 'ch-1',
      payload: new Uint8Array(1),
      maxReassembledMessageSize: 100,
    })
    await yieldToEventLoop()

    expect(secretsZero(state)).toBe(true)
    expect(posted.map(event => event.type === 'requestFailed'
      ? [event.requestId, event.error.message]
      : event.type)).toEqual([
      [4, 'the message cannot be cloned'],
      [5, 'handshake is gone'],
    ])
  })

  it('registers the session on finishHandshake and answers requestDone', async () => {
    const h = endpointHarness()

    await openSession(h)

    expect(eventsOf(h.posted, 'requestDone')).toEqual([
      { type: 'requestDone', requestId: 2 },
    ])
    const state = h.endpoint.sessionState('ch-1')
    expect(state?.session).toBe(h.pair.initiator)
    expect(state?.maxReassembledMessageSize).toBe(1 << 20)
    // A post-quantum session keeps the Worker's ML-KEM key for its rekeys.
    expect(state?.workerMlkemPub.byteLength).toBe(1568)
  })

  it('keeps no ML-KEM key for a classic session', async () => {
    const h = endpointHarness({
      classicHandshake1: () => ({
        message1: new Uint8Array(48),
        handshakeState: blankClassicHandshakeState(),
      }),
      classicHandshake2: () => createTestSession().initiator,
    })

    h.endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 1,
      mode: EncryptionMode.CLASSIC,
      keys,
    })
    h.endpoint.handleCommand({
      type: 'finishHandshake',
      requestId: 2,
      handshakeId: 1,
      channelId: 'ch-1',
      payload: new Uint8Array(48),
      maxReassembledMessageSize: 100,
    })
    await yieldToEventLoop()

    expect(h.endpoint.sessionState('ch-1')?.workerMlkemPub.byteLength).toBe(0)
  })

  it('answers requestFailed with the cause for a failing handshake 2, registers nothing, and zeroes the state', async () => {
    const h = endpointHarness({
      handshake2: async () => {
        throw new Error('noise-hybrid: SLH-DSA signature verification failed')
      },
    })
    h.endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 1,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })
    fillSecrets(h.states[0]!)

    h.endpoint.handleCommand({
      type: 'finishHandshake',
      requestId: 2,
      handshakeId: 1,
      channelId: 'ch-1',
      payload: new Uint8Array(8),
      maxReassembledMessageSize: 100,
    })
    await yieldToEventLoop()

    expect(eventsOf(h.posted, 'requestFailed')).toEqual([
      {
        type: 'requestFailed',
        requestId: 2,
        error: {
          source: 'transport',
          message: 'noise-hybrid: SLH-DSA signature verification failed',
          code: 0,
          fatal: false,
          disconnected: true,
        },
      },
    ])
    expect(h.endpoint.sessionState('ch-1')).toBeUndefined()
    expect(secretsZero(h.states[0]!)).toBe(true)
  })

  it('answers requestFailed for an unknown handshake id', async () => {
    const h = endpointHarness()

    h.endpoint.handleCommand({
      type: 'finishHandshake',
      requestId: 9,
      handshakeId: 77,
      channelId: 'ch-1',
      payload: new Uint8Array(1),
      maxReassembledMessageSize: 100,
    })
    await yieldToEventLoop()

    expect(eventsOf(h.posted, 'requestFailed')).toEqual([
      {
        type: 'requestFailed',
        requestId: 9,
        error: {
          source: 'transport',
          message: 'handshake is gone',
          code: 0,
          fatal: false,
          disconnected: true,
        },
      },
    ])
  })

  it('refuses a second session for a channel, and zeroes the second handshake', async () => {
    const h = endpointHarness()
    await openSession(h)
    h.endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 3,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })
    fillSecrets(h.states[1]!)

    h.endpoint.handleCommand({
      type: 'finishHandshake',
      requestId: 4,
      handshakeId: 3,
      channelId: 'ch-1',
      payload: new Uint8Array(1),
      maxReassembledMessageSize: 100,
    })
    await yieldToEventLoop()

    expect(eventsOf(h.posted, 'requestFailed').map(event => event.error.message)).toEqual(['channel session ch-1 already exists'])
    expect(secretsZero(h.states[1]!)).toBe(true)
    expect(h.endpoint.sessionState('ch-1')?.session).toBe(h.pair.initiator)
  })

  // Both finishes pass the check before their handshake 2, so only the check
  // after it can refuse the later one.
  it('refuses the later of two finishes that register one channel at the same time, and destroys its keys', async () => {
    const established = [
      createTestSession(),
      createTestSession(),
    ]
    let finishes = 0
    const h = endpointHarness({
      handshake2: async () => established[finishes++]!.initiator,
    })
    for (const requestId of [1, 2]) {
      h.endpoint.handleCommand({
        type: 'beginHandshake',
        requestId,
        mode: EncryptionMode.POST_QUANTUM,
        keys,
      })
    }

    for (const [requestId, handshakeId] of [
      [3, 1],
      [4, 2],
    ] as const) {
      h.endpoint.handleCommand({
        type: 'finishHandshake',
        requestId,
        handshakeId,
        channelId: 'ch-1',
        payload: new Uint8Array(8),
        maxReassembledMessageSize: 100,
      })
    }
    await yieldToEventLoop()

    expect(eventsOf(h.posted, 'requestDone').map(event => event.requestId)).toEqual([3])
    expect(eventsOf(h.posted, 'requestFailed')).toEqual([
      {
        type: 'requestFailed',
        requestId: 4,
        error: {
          source: 'transport',
          message: 'channel session ch-1 already exists',
          code: 0,
          fatal: false,
          disconnected: true,
        },
      },
    ])
    expect(h.endpoint.sessionState('ch-1')?.session).toBe(established[0]!.initiator)
    expect(() => established[1]!.initiator.send.encrypt(new Uint8Array(1))).toThrow('noise: cipher state destroyed')
    expect(() => established[1]!.initiator.receive.decrypt(new Uint8Array(17))).toThrow('noise: cipher state destroyed')
  })

  it('zeroes the state on abandonHandshake, and ignores an unknown id', () => {
    const h = endpointHarness()
    h.endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 1,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })
    fillSecrets(h.states[0]!)

    h.endpoint.handleCommand({ type: 'abandonHandshake', handshakeId: 1 })
    h.endpoint.handleCommand({ type: 'abandonHandshake', handshakeId: 99 })

    expect(secretsZero(h.states[0]!)).toBe(true)
    expect(h.posted).toHaveLength(1)
  })
})

describe('createChannelSessionEndpoint send', () => {
  it('posts one wire event per chunk, in nonce order, each with its buffer in the transfer list', async () => {
    const h = endpointHarness()
    await openSession(h)
    const plaintext = Uint8Array.from({ length: MAX_CHUNK_SIZE * 2 + 10 }, (_, i) => i % 251)

    h.endpoint.handleCommand({
      type: 'send',
      channelId: 'ch-1',
      correlationId: 2,
      plaintext,
      startsRequest: true,
    })

    const wires = h.posted.filter(entry => entry.event.type === 'wire')
    expect(wires).toHaveLength(3)
    for (const { event, transfer } of wires)
      expect(transfer).toEqual([(event as Extract<SessionEvent, { type: 'wire' }>).frame.buffer])
    // The Worker decrypts them in wire order, so the nonces match that order.
    const read = wires.map(({ event }) => workerReads(h, (event as Extract<SessionEvent, { type: 'wire' }>).frame))
    expect(read.map(({ message }) => message.flags)).toEqual([ChannelMessageFlags.MORE, ChannelMessageFlags.MORE, ChannelMessageFlags.UNSPECIFIED])
    const joined = new Uint8Array(read.reduce((total, { plaintext: chunk }) => total + chunk.length, 0))
    let offset = 0
    for (const { plaintext: chunk } of read) {
      joined.set(chunk, offset)
      offset += chunk.length
    }
    expect([...joined]).toEqual([...plaintext])
  })

  it('keeps the frames of two messages contiguous and in arrival order', async () => {
    const h = endpointHarness()
    await openSession(h)

    h.endpoint.handleCommand({
      type: 'send',
      channelId: 'ch-1',
      correlationId: 2,
      plaintext: new Uint8Array(MAX_CHUNK_SIZE + 1),
      startsRequest: true,
    })
    h.endpoint.handleCommand({
      type: 'send',
      channelId: 'ch-1',
      correlationId: 3,
      plaintext: new Uint8Array(MAX_CHUNK_SIZE + 1),
      startsRequest: true,
    })

    const ids = eventsOf(h.posted, 'wire').map(({ frame }) => Number(workerReads(h, frame).message.correlationId))
    expect(ids).toEqual([2, 2, 3, 3])
  })

  it('posts sessionEnded for a send to an unknown channel', () => {
    const h = endpointHarness()

    h.endpoint.handleCommand({
      type: 'send',
      channelId: 'ch-gone',
      correlationId: 2,
      plaintext: new Uint8Array(1),
      startsRequest: true,
    })

    expect(eventsOf(h.posted, 'sessionEnded')).toEqual([
      {
        type: 'sessionEnded',
        channelId: 'ch-gone',
        error: {
          source: 'transport',
          message: 'channel session is gone',
          code: 0,
          fatal: false,
          disconnected: true,
        },
      },
    ])
  })
})

describe('createChannelSessionEndpoint inbound', () => {
  it('delivers a reply for a live id, with the plaintext buffer in the transfer list', async () => {
    const h = endpointHarness()
    await openSession(h)
    h.endpoint.handleCommand({
      type: 'send',
      channelId: 'ch-1',
      correlationId: 2,
      plaintext: new Uint8Array(1),
      startsRequest: true,
    })

    workerSends(h, 2, response)

    const delivers = h.posted.filter(entry => entry.event.type === 'deliver')
    expect(delivers).toHaveLength(1)
    const event = delivers[0]!.event as Extract<SessionEvent, { type: 'deliver' }>
    expect(event.correlationId).toBe(2)
    expect(fromBinary(InnerMessageSchema, event.plaintext).kind.case).toBe('response')
    expect(delivers[0]!.transfer).toEqual([event.plaintext.buffer])
    expect(event.plaintext.byteOffset).toBe(0)
    expect(event.plaintext.byteLength).toBe(event.plaintext.buffer.byteLength)
  })

  it('keeps a tombstone after a release of a poisoned id', async () => {
    const h = endpointHarness()
    await openSession(h, 'ch-1', 1, 50)
    h.endpoint.handleCommand({
      type: 'send',
      channelId: 'ch-1',
      correlationId: 2,
      plaintext: new Uint8Array(1),
      startsRequest: true,
    })
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    workerSendsChunk(h, 2, new Uint8Array(30), ChannelMessageFlags.MORE)
    workerSendsChunk(h, 2, new Uint8Array(30), ChannelMessageFlags.MORE)
    expect(eventsOf(h.posted, 'reassemblyFailed')).toEqual([
      {
        type: 'reassemblyFailed',
        channelId: 'ch-1',
        correlationId: 2,
        message: 'chunked message too large: 60 bytes exceeds 50 byte limit',
      },
    ])
    // The main thread fails the request, which releases its id.
    h.endpoint.handleCommand({ type: 'release', channelId: 'ch-1', correlationId: 2 })
    workerSendsChunk(h, 2, new Uint8Array(30), ChannelMessageFlags.MORE)
    workerSendsChunk(h, 2, new Uint8Array(1), ChannelMessageFlags.UNSPECIFIED)

    expect(eventsOf(h.posted, 'reassemblyFailed')).toHaveLength(1)
    expect(eventsOf(h.posted, 'deliver')).toEqual([])
    expect(eventsOf(h.posted, 'sessionEnded')).toEqual([])
    errorSpy.mockRestore()
  })

  it('ends the session with a decrypt error on a frame that does not decrypt', async () => {
    const h = endpointHarness()
    await openSession(h)
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    h.endpoint.handleCommand({
      type: 'inbound',
      channelId: 'ch-1',
      correlationId: 2n,
      flags: ChannelMessageFlags.UNSPECIFIED,
      ciphertext: new Uint8Array(20),
    })

    const ended = eventsOf(h.posted, 'sessionEnded')
    expect(ended).toHaveLength(1)
    expect(ended[0]!.error).toMatchObject({ source: 'transport', disconnected: false })
    expect(ended[0]!.error.message).toMatch(/^decrypt a channel message: /)
    expect(h.endpoint.sessionState('ch-1')).toBeUndefined()
    errorSpy.mockRestore()
  })
})

describe('createChannelSessionEndpoint ensureReady', () => {
  it('answers at once when no rekey is due', async () => {
    const h = endpointHarness()
    await openSession(h)

    h.endpoint.handleCommand({ type: 'ensureReady', requestId: 10, channelId: 'ch-1' })
    await yieldToEventLoop()

    expect(eventsOf(h.posted, 'requestDone').map(event => event.requestId)).toEqual([2, 10])
    expect(eventsOf(h.posted, 'wire')).toEqual([])
  })

  it('sends one RekeyRequest for two concurrent ensureReady commands, and answers both after the Ack', async () => {
    const h = endpointHarness()
    await openSession(h)
    age(h)

    h.endpoint.handleCommand({ type: 'ensureReady', requestId: 10, channelId: 'ch-1' })
    h.endpoint.handleCommand({ type: 'ensureReady', requestId: 11, channelId: 'ch-1' })
    await yieldToEventLoop()

    expect(eventsOf(h.posted, 'wire')).toHaveLength(1)
    expect(eventsOf(h.posted, 'requestDone').map(event => event.requestId)).toEqual([2])

    workerAcksRekey(h)
    await yieldToEventLoop()

    expect(eventsOf(h.posted, 'requestDone').map(event => event.requestId)).toEqual([2, 10, 11])
    expect(eventsOf(h.posted, 'wire')).toHaveLength(1)
  })

  it('applies the reject backoff, and answers requestDone after a Reject', async () => {
    const h = endpointHarness()
    await openSession(h)
    age(h)
    h.endpoint.handleCommand({ type: 'ensureReady', requestId: 10, channelId: 'ch-1' })
    await yieldToEventLoop()
    const { message } = workerReads(h, eventsOf(h.posted, 'wire').at(-1)!.frame)
    const before = performance.now()

    workerSends(h, Number(message.correlationId), {
      kind: {
        case: 'rekeyReject',
        value: create(RekeyRejectSchema, { retryAfterMs: 30_000n }),
      },
    })
    await yieldToEventLoop()

    expect(eventsOf(h.posted, 'requestDone').map(event => event.requestId)).toEqual([2, 10])
    expect(h.endpoint.sessionState('ch-1')!.rekeyNotBefore).toBeGreaterThanOrEqual(before + 30_000 - 5)
  })

  it('posts sessionEnded, then requestFailed, for ensureReady past the hard ceiling', async () => {
    const h = endpointHarness()
    await openSession(h)
    age(h, 'ch-1', SESSION_KEY_HARD_CEILING_MS)

    h.endpoint.handleCommand({ type: 'ensureReady', requestId: 10, channelId: 'ch-1' })
    await yieldToEventLoop()

    const tail = h.posted.slice(-2).map(entry => entry.event)
    expect(tail).toEqual([
      {
        type: 'sessionEnded',
        channelId: 'ch-1',
        error: {
          source: 'transport',
          message: 'session key past hard ceiling',
          code: 0,
          fatal: false,
          disconnected: false,
        },
      },
      {
        type: 'requestFailed',
        requestId: 10,
        error: {
          source: 'transport',
          message: 'session key past hard ceiling',
          code: 0,
          fatal: false,
          disconnected: false,
        },
      },
    ])
    expect(h.endpoint.sessionState('ch-1')).toBeUndefined()
  })

  it('posts sessionEnded and requestFailed for ensureReady on an unknown channel', async () => {
    const h = endpointHarness()

    h.endpoint.handleCommand({ type: 'ensureReady', requestId: 10, channelId: 'ch-gone' })
    await yieldToEventLoop()

    expect(h.posted.map(entry => entry.event.type)).toEqual(['sessionEnded', 'requestFailed'])
  })

  it('ends the session with "rekey timeout" when no Ack arrives in time, and drops the held sends', async () => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout'],
    })
    const h = endpointHarness()
    await openSession(h)
    age(h)
    h.endpoint.handleCommand({
      type: 'send',
      channelId: 'ch-1',
      correlationId: 2,
      plaintext: new Uint8Array(1),
      startsRequest: true,
    })
    await yieldToEventLoop()
    expect(eventsOf(h.posted, 'wire')).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(SESSION_VERIFY_TIMEOUT_MS - 1)
    expect(eventsOf(h.posted, 'sessionEnded')).toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    await yieldToEventLoop()

    expect(eventsOf(h.posted, 'sessionEnded').map(event => event.error.message)).toEqual(['rekey timeout'])
    // The held send never reached the wire.
    expect(eventsOf(h.posted, 'wire')).toHaveLength(1)
    expect(h.endpoint.sessionState('ch-1')).toBeUndefined()
  })
})

describe('createChannelSessionEndpoint lifecycle', () => {
  it('posts no event for close, after which an inbound frame for that channel does nothing', async () => {
    const h = endpointHarness()
    await openSession(h)
    const before = h.posted.length

    h.endpoint.handleCommand({ type: 'close', channelId: 'ch-1' })
    workerSends(h, 2, response)

    expect(h.posted).toHaveLength(before)
    expect(h.endpoint.sessionState('ch-1')).toBeUndefined()
    // The closed session's keys are zero.
    expect(() => h.pair.initiator.send.encrypt(new Uint8Array(1))).toThrow(/destroyed/)
  })

  it('starts the idle timer with the first session and stops it after the last close', async () => {
    vi.useFakeTimers({
      toFake: ['setInterval', 'clearInterval'],
    })
    const h = endpointHarness({ idleRekeyIntervalMs: 60_000 })
    expect(vi.getTimerCount()).toBe(0)

    await openSession(h, 'ch-1', 1)
    expect(vi.getTimerCount()).toBe(1)
    await openSession(h, 'ch-2', 3)
    expect(vi.getTimerCount()).toBe(1)

    h.endpoint.handleCommand({ type: 'close', channelId: 'ch-1' })
    expect(vi.getTimerCount()).toBe(1)
    h.endpoint.handleCommand({ type: 'close', channelId: 'ch-2' })
    expect(vi.getTimerCount()).toBe(0)
  })

  it('starts no idle timer when the interval is 0', async () => {
    vi.useFakeTimers({
      toFake: ['setInterval', 'clearInterval'],
    })
    const h = endpointHarness({ idleRekeyIntervalMs: 0 })

    await openSession(h)

    expect(vi.getTimerCount()).toBe(0)
  })

  it('rekeys an aged session on an idle tick', async () => {
    vi.useFakeTimers({
      toFake: ['setInterval', 'clearInterval'],
    })
    const h = endpointHarness({ idleRekeyIntervalMs: 60_000 })
    await openSession(h)
    age(h)

    await vi.advanceTimersByTimeAsync(60_000)

    const wires = eventsOf(h.posted, 'wire')
    expect(wires).toHaveLength(1)
    expect(Number(workerReads(h, wires[0]!.frame).message.correlationId)).toBe(FIRST_REKEY_CORRELATION_ID)
  })

  it('rekeys an aged session on checkRekey, and leaves a fresh one alone', async () => {
    const h = endpointHarness()
    await openSession(h)

    h.endpoint.handleCommand({ type: 'checkRekey' })
    await yieldToEventLoop()
    expect(eventsOf(h.posted, 'wire')).toEqual([])

    age(h)
    h.endpoint.handleCommand({ type: 'checkRekey' })
    await yieldToEventLoop()
    const wires = eventsOf(h.posted, 'wire')
    expect(wires).toHaveLength(1)
    expect(fromBinary(InnerMessageSchema, workerReads(h, wires[0]!.frame).plaintext).kind.case).toBe('rekeyRequest')
  })

  it('zeroes every session and pending handshake on shutdown, then calls closeSelf and ignores later commands', async () => {
    const h = endpointHarness()
    await openSession(h)
    h.endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 3,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })
    fillSecrets(h.states[1]!)
    const before = h.posted.length

    h.endpoint.handleCommand({ type: 'shutdown' })

    expect(h.closeSelf).toHaveBeenCalledOnce()
    expect(() => h.pair.initiator.send.encrypt(new Uint8Array(1))).toThrow(/destroyed/)
    expect(() => h.pair.initiator.receive.decrypt(new Uint8Array(17))).toThrow(/destroyed/)
    expect(secretsZero(h.states[1]!)).toBe(true)
    expect(h.endpoint.sessionState('ch-1')).toBeUndefined()

    h.endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 5,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })
    h.endpoint.handleCommand({
      type: 'send',
      channelId: 'ch-1',
      correlationId: 2,
      plaintext: new Uint8Array(1),
      startsRequest: true,
    })
    expect(h.posted).toHaveLength(before)
  })

  it('destroys a session whose finishHandshake completes after shutdown, and answers nothing', async () => {
    let release!: (session: Session) => void
    const finished = new Promise<Session>((resolve) => {
      release = resolve
    })
    const pair = createTestSession()
    const h = endpointHarness({
      handshake2: () => finished,
    })
    h.endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 1,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })
    h.endpoint.handleCommand({
      type: 'finishHandshake',
      requestId: 2,
      handshakeId: 1,
      channelId: 'ch-1',
      payload: new Uint8Array(1),
      maxReassembledMessageSize: 100,
    })
    const before = h.posted.length

    h.endpoint.handleCommand({ type: 'shutdown' })
    release(pair.initiator)
    await yieldToEventLoop()

    expect(() => pair.initiator.send.encrypt(new Uint8Array(1))).toThrow(/destroyed/)
    expect(h.endpoint.sessionState('ch-1')).toBeUndefined()
    expect(h.posted).toHaveLength(before)
  })

  it('ends the session of a channel whose command throws, and tells the main thread', async () => {
    const { send } = createTestSession().initiator
    const broken: CipherStateLike = {
      encrypt: plaintext => send.encrypt(plaintext),
      decrypt: ciphertext => send.decrypt(ciphertext),
      needsRekey: () => {
        throw new Error('the cipher state broke')
      },
      rekeyWithSecret: (dhSecret, pqSecret, retainPrev) => send.rekeyWithSecret(dhSecret, pqSecret, retainPrev),
      clearPrev: () => send.clearPrev(),
      nonce: () => send.nonce(),
      destroy: () => send.destroy(),
    }
    const pair = createTestSession()
    const h = endpointHarness({
      handshake2: async () => ({ send: broken, receive: pair.initiator.receive }),
    })
    await openSession(h)

    h.endpoint.handleCommand({
      type: 'send',
      channelId: 'ch-1',
      correlationId: 2,
      plaintext: new Uint8Array(1),
      startsRequest: true,
    })

    expect(eventsOf(h.posted, 'sessionEnded')).toEqual([
      {
        type: 'sessionEnded',
        channelId: 'ch-1',
        error: {
          source: 'transport',
          message: 'the cipher state broke',
          code: 0,
          fatal: false,
          disconnected: true,
        },
      },
    ])
    expect(h.endpoint.sessionState('ch-1')).toBeUndefined()
    expect(() => send.encrypt(new Uint8Array(1))).toThrow('noise: cipher state destroyed')
  })

  it('logs a command that throws for no request and no channel, and keeps serving', () => {
    let begins = 0
    const h = endpointHarness({
      // A state without its secrets, which the zeroing of an abandon cannot read.
      handshake1: () => ({
        message1: new Uint8Array(1616),
        handshakeState: begins++ === 0
          ? {} as HybridHandshakeState
          : blankHybridHandshakeState(),
      }),
    })
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    h.endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 1,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })

    expect(() => h.endpoint.handleCommand({ type: 'abandonHandshake', handshakeId: 1 })).not.toThrow()

    expect(errorSpy).toHaveBeenCalledWith('[channel]', 'the channel session worker failed to handle a command', {
      command: 'abandonHandshake',
      error: expect.any(TypeError),
    })
    h.endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 2,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })
    expect(eventsOf(h.posted, 'handshakeBegun').map(event => event.requestId)).toEqual([1, 2])
    errorSpy.mockRestore()
  })

  it('logs a failure that it cannot report, and throws nothing into the message dispatch', () => {
    const endpoint = createChannelSessionEndpoint({
      post: () => {
        throw new Error('the port closed')
      },
      closeSelf: () => {},
    }, {
      handshake1: () => ({
        message1: new Uint8Array(1616),
        handshakeState: blankHybridHandshakeState(),
      }),
      idleRekeyIntervalMs: 0,
    })
    endpoints.push(endpoint)
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    expect(() => endpoint.handleCommand({
      type: 'beginHandshake',
      requestId: 4,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    })).not.toThrow()

    expect(errorSpy).toHaveBeenCalledWith('[channel]', 'the channel session worker failed to report a failure', {
      command: 'beginHandshake',
      error: expect.objectContaining({ message: 'the port closed' }),
      report_error: expect.objectContaining({ message: 'the port closed' }),
    })
    errorSpy.mockRestore()
  })

  it('sets the debug flag on configure', () => {
    const was = isDebugEnabled()
    try {
      const h = endpointHarness()
      h.endpoint.handleCommand({ type: 'configure', debug: !was })
      expect(isDebugEnabled()).toBe(!was)
      h.endpoint.handleCommand({ type: 'configure', debug: was })
      expect(isDebugEnabled()).toBe(was)
    }
    finally {
      setDebugEnabled(was)
    }
  })
})
