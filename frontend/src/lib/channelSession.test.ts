import type { ChannelSessionOpts, SessionChannel } from './channelSession'
import type { HandshakeState, Session } from './noise'
import type { HybridHandshakeState } from './noise-hybrid'
import { fromBinary } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { DEFAULT_REJECT_BACKOFF_MS, MAX_CHUNK_SIZE, SESSION_KEY_HARD_CEILING_MS, SESSION_KEY_MAX_AGE_MS } from '~/generated/contracts/wire'
import { ChannelMessageFlags, ChannelMessageSchema, EncryptionMode, InnerMessageSchema } from '~/generated/proto/leapmux/v1/channel_pb'
import { blankClassicHandshakeState, blankHybridHandshakeState } from '~/test-support/channelSessionDoubles'
import { ChannelError } from './channelError'
import { unframeBytes } from './channelFraming'
import { ChannelSession, createSessionChannel, FIRST_REKEY_CORRELATION_ID } from './channelSession'
import { generateRekeyEphemeral } from './noise-hybrid'
import { verifySlhDsaHere } from './slhDsaVerifyHere'
import { yieldToEventLoop } from './workSlices'

/** A message of `count` chunks: full chunks, then a last chunk of 10 bytes. */
function chunks(count: number): Uint8Array {
  return new Uint8Array(MAX_CHUNK_SIZE * (count - 1) + 10)
}

function mockCipher(sendNeeds = false, recvNeeds = false) {
  const encrypt = vi.fn((pt: Uint8Array) => new Uint8Array(pt))
  const decrypt = vi.fn((ct: Uint8Array) => new Uint8Array(ct))
  // Separate mocks per direction so tests can assert the send-vs-recv asymmetry
  // (e.g. clearPrev is called on send only). `satisfies Session` (not
  // `as unknown as`) so a future CipherState method addition fails to compile
  // here instead of silently diverging the mock from production. Each half
  // carries the full CipherStateLike surface; the direction under test uses the
  // real mock, the unused direction's method is a no-op never called.
  const noop = vi.fn()
  return {
    send: {
      encrypt,
      decrypt: noop,
      needsRekey: () => sendNeeds,
      rekeyWithSecret: vi.fn(),
      clearPrev: vi.fn(),
      nonce: () => 0,
      destroy: vi.fn(),
    },
    receive: {
      encrypt: noop,
      decrypt,
      needsRekey: () => recvNeeds,
      rekeyWithSecret: vi.fn(),
      clearPrev: vi.fn(),
      nonce: () => 0,
      destroy: vi.fn(),
    },
  } satisfies Session
}

/** A fresh session record, as the endpoint registers one, with `overrides` applied. */
function makeChannel(overrides?: Partial<SessionChannel>): SessionChannel {
  return {
    ...createSessionChannel({
      channelId: 'ch-1',
      session: overrides?.session ?? mockCipher(),
      maxReassembledMessageSize: 1024,
      workerMlkemPub: new Uint8Array(0),
    }),
    ...overrides,
  }
}

/** A session record whose key is past its max age, so a rekey is due. */
function agedChannel(overrides?: Partial<SessionChannel>): SessionChannel {
  return makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_MAX_AGE_MS - 1, ...overrides })
}

/**
 * A ChannelSession whose deps record each posted frame and each session end.
 * `endSession` disposes the session first, as the endpoint does. `beforePost`
 * runs before a frame is recorded, and may throw to fail the post.
 */
function harness(opts?: ChannelSessionOpts, beforePost?: (frame: Uint8Array) => void) {
  const frames: Uint8Array[] = []
  const ended: ChannelError[] = []
  const session = new ChannelSession({
    postFrame: (_channelId, frame) => {
      beforePost?.(frame)
      frames.push(frame)
    },
    endSession: (ch, error) => endSession(ch, error),
  }, opts)
  function endSession(ch: SessionChannel, error: ChannelError): void {
    if (ch.ended)
      return
    session.dispose(ch)
    ended.push(error)
  }
  return { session, frames, ended }
}

/** The message of each posted frame. */
function decodeMessages(frames: Uint8Array[]) {
  return frames.map((buf) => {
    const unframed = unframeBytes(buf)
    if (!unframed.ok)
      throw new Error(`a frame failed to unframe: ${unframed.failure.kind}`)
    return fromBinary(ChannelMessageSchema, unframed.payload)
  })
}

/** The correlation ID and the flags of each posted frame. */
function decodeFrames(frames: Uint8Array[]): Array<{ id: number, more: boolean }> {
  return decodeMessages(frames).map(message => ({
    id: Number(message.correlationId),
    more: message.flags === ChannelMessageFlags.MORE,
  }))
}

/** The InnerMessage kind of each posted frame, under the identity mock cipher. */
function frameKinds(frames: Uint8Array[]): Array<string | undefined> {
  return decodeMessages(frames).map(message => fromBinary(InnerMessageSchema, message.ciphertext).kind.case)
}

/** Whether every byte of `bytes` is zero. */
function allZero(bytes: Uint8Array): boolean {
  return bytes.every(byte => byte === 0)
}

/** Put non-zero bytes into every secret of a classic handshake state, so that a test can see them zeroed. */
function fillClassicSecrets(state: HandshakeState): void {
  state.e.privateKey.fill(0x11)
  state.ss.ck.fill(0x22)
  state.ss.h.fill(0x33)
  state.ss.k.fill(0x44)
}

/** Put non-zero bytes into every secret of a hybrid handshake state (see fillClassicSecrets). */
function fillHybridSecrets(state: HybridHandshakeState): void {
  fillClassicSecrets(state)
  state.mlkemSS.fill(0x55)
  state.mlkemCT.fill(0x66)
}

/** Whether every secret of a classic handshake state is zero. */
function classicSecretsZero(state: HandshakeState): boolean {
  return allZero(state.e.privateKey) && allZero(state.ss.ck) && allZero(state.ss.h) && allZero(state.ss.k)
}

/** Whether every secret of a hybrid handshake state is zero. */
function hybridSecretsZero(state: HybridHandshakeState): boolean {
  return classicSecretsZero(state) && allZero(state.mlkemSS) && allZero(state.mlkemCT)
}

const keys = {
  x25519PublicKey: new Uint8Array([1]),
  mlkemPublicKey: new Uint8Array([2]),
  slhdsaPublicKey: new Uint8Array([3]),
}

describe('ChannelSession beginHandshake', () => {
  it('uses classic Noise for CLASSIC mode', async () => {
    const message1 = new Uint8Array([9, 9])
    const finished = mockCipher()
    const state = blankClassicHandshakeState()
    const classicHandshake1 = vi.fn((_rs: Uint8Array) => ({ message1, handshakeState: state }))
    const classicHandshake2 = vi.fn((_hs: HandshakeState, _payload: Uint8Array) => finished)
    const hybrid1 = vi.fn(() => ({
      message1,
      handshakeState: blankHybridHandshakeState(),
    }))
    const { session } = harness({ classicHandshake1, classicHandshake2, handshake1: hybrid1 })

    const pending = session.beginHandshake(EncryptionMode.CLASSIC, keys)
    expect(classicHandshake1).toHaveBeenCalledWith(keys.x25519PublicKey)
    expect(hybrid1).not.toHaveBeenCalled()
    expect(pending.message1).toBe(message1)

    const payload = new Uint8Array([4, 5])
    await expect(pending.finish(payload)).resolves.toBe(finished)
    expect(classicHandshake2).toHaveBeenCalledWith(state, payload)
  })

  it('uses hybrid Noise for post-quantum mode, and verifies on the worker thread', async () => {
    const message1 = new Uint8Array([8, 8])
    const finished = mockCipher()
    const state = blankHybridHandshakeState()
    const handshake1 = vi.fn((_x: Uint8Array, _m: Uint8Array) => ({ message1, handshakeState: state }))
    const handshake2 = vi.fn(async (_hs: HybridHandshakeState, _payload: Uint8Array, _slh: Uint8Array, _verify: unknown) => finished)
    const classic1 = vi.fn(() => ({
      message1,
      handshakeState: blankClassicHandshakeState(),
    }))
    const { session } = harness({ handshake1, handshake2, classicHandshake1: classic1 })

    const pending = session.beginHandshake(EncryptionMode.POST_QUANTUM, keys)
    expect(handshake1).toHaveBeenCalledWith(keys.x25519PublicKey, keys.mlkemPublicKey)
    expect(classic1).not.toHaveBeenCalled()
    expect(pending.message1).toBe(message1)

    const payload = new Uint8Array([6, 7])
    await expect(pending.finish(payload)).resolves.toBe(finished)
    // The session runs in the session worker, so it verifies on its own thread.
    expect(handshake2).toHaveBeenCalledWith(state, payload, keys.slhdsaPublicKey, verifySlhDsaHere)
  })

  it('treats UNSPECIFIED like post-quantum (hybrid path)', () => {
    const handshake1 = vi.fn((_x: Uint8Array, _m: Uint8Array) => ({
      message1: new Uint8Array([1]),
      handshakeState: blankHybridHandshakeState(),
    }))
    const classic1 = vi.fn(() => ({
      message1: new Uint8Array([1]),
      handshakeState: blankClassicHandshakeState(),
    }))
    const { session } = harness({ handshake1, classicHandshake1: classic1 })
    session.beginHandshake(EncryptionMode.UNSPECIFIED, keys)
    expect(handshake1).toHaveBeenCalledOnce()
    expect(classic1).not.toHaveBeenCalled()
  })

  it('propagates a hybrid handshake-2 failure from the finish, and zeroes the state', async () => {
    const state = blankHybridHandshakeState()
    fillHybridSecrets(state)
    const { session } = harness({
      handshake1: () => ({
        message1: new Uint8Array([1]),
        handshakeState: state,
      }),
      handshake2: () => {
        throw new Error('bad handshake-2')
      },
    })
    const pending = session.beginHandshake(EncryptionMode.POST_QUANTUM, keys)

    await expect(pending.finish(new Uint8Array([1]))).rejects.toThrow('bad handshake-2')
    expect(hybridSecretsZero(state)).toBe(true)
  })

  it('propagates a classic handshake-2 failure from the finish, and zeroes the state', async () => {
    const state = blankClassicHandshakeState()
    fillClassicSecrets(state)
    const { session } = harness({
      classicHandshake1: () => ({
        message1: new Uint8Array([1]),
        handshakeState: state,
      }),
      classicHandshake2: () => {
        throw new Error('bad classic handshake-2')
      },
    })
    const pending = session.beginHandshake(EncryptionMode.CLASSIC, keys)

    await expect(pending.finish(new Uint8Array([1]))).rejects.toThrow('bad classic handshake-2')
    expect(classicSecretsZero(state)).toBe(true)
  })

  it('zeroes the state after a finish that succeeds', async () => {
    const state = blankHybridHandshakeState()
    fillHybridSecrets(state)
    const { session } = harness({
      handshake1: () => ({
        message1: new Uint8Array([1]),
        handshakeState: state,
      }),
      handshake2: async () => mockCipher(),
    })

    await session.beginHandshake(EncryptionMode.POST_QUANTUM, keys).finish(new Uint8Array([1]))

    expect(hybridSecretsZero(state)).toBe(true)
  })

  it('zeroes the classic and the hybrid handshake state in abandon, once', () => {
    const classic = blankClassicHandshakeState()
    const hybrid = blankHybridHandshakeState()
    fillClassicSecrets(classic)
    fillHybridSecrets(hybrid)
    const { session } = harness({
      classicHandshake1: () => ({
        message1: new Uint8Array([1]),
        handshakeState: classic,
      }),
      handshake1: () => ({
        message1: new Uint8Array([2]),
        handshakeState: hybrid,
      }),
    })

    const classicPending = session.beginHandshake(EncryptionMode.CLASSIC, keys)
    const hybridPending = session.beginHandshake(EncryptionMode.POST_QUANTUM, keys)
    classicPending.abandon()
    hybridPending.abandon()
    expect(() => hybridPending.abandon()).not.toThrow()

    expect(classicSecretsZero(classic)).toBe(true)
    expect(hybridSecretsZero(hybrid)).toBe(true)
  })

  it('refuses to finish a handshake that was abandoned, and never runs handshake 2', async () => {
    const handshake2 = vi.fn(async () => mockCipher())
    const { session } = harness({
      handshake1: () => ({
        message1: new Uint8Array([1]),
        handshakeState: blankHybridHandshakeState(),
      }),
      handshake2,
    })
    const pending = session.beginHandshake(EncryptionMode.POST_QUANTUM, keys)
    pending.abandon()

    await expect(pending.finish(new Uint8Array([1]))).rejects.toThrow(/already finished or was abandoned/)
    expect(handshake2).not.toHaveBeenCalled()
  })
})

describe('ChannelSession policy', () => {
  it('decrypt calls through to the receive cipher', () => {
    const cipher = mockCipher()
    const { session } = harness()
    const ct = new Uint8Array([10, 11, 12])
    expect(session.decrypt(makeChannel({ session: cipher }), ct)).toEqual(ct)
    expect(cipher.receive.decrypt).toHaveBeenCalledWith(ct)
  })

  it('decrypt propagates cipher failures so the inbound path can end the session', () => {
    const cipher = mockCipher()
    cipher.receive.decrypt.mockImplementation(() => {
      throw new Error('tag mismatch')
    })
    const { session } = harness()
    expect(() => session.decrypt(makeChannel({ session: cipher }), new Uint8Array([1]))).toThrow('tag mismatch')
  })

  it('returns true from shouldInitiateRekey on soft nonce or age', () => {
    const { session } = harness()
    expect(session.shouldInitiateRekey(makeChannel({
      session: mockCipher(true, false),
    }))).toBe(true)
    expect(session.shouldInitiateRekey(agedChannel())).toBe(true)
    expect(session.shouldInitiateRekey(makeChannel())).toBe(false)
  })

  it('respects reject backoff in shouldInitiateRekey unless soft nonce', () => {
    const { session } = harness()
    const now = performance.now()
    const backedOff = makeChannel({
      lastRekeyAt: now - SESSION_KEY_MAX_AGE_MS - 1,
      rekeyNotBefore: now + 60_000,
    })
    expect(session.shouldInitiateRekey(backedOff, now)).toBe(false)

    const soft = makeChannel({
      session: mockCipher(true, false),
      lastRekeyAt: now - SESSION_KEY_MAX_AGE_MS - 1,
      rekeyNotBefore: now + 60_000,
    })
    expect(session.shouldInitiateRekey(soft, now)).toBe(true)
  })

  it('tracks absolute age in pastHardCeiling', () => {
    const { session } = harness()
    const now = performance.now()
    expect(session.pastHardCeiling(makeChannel({ lastRekeyAt: now }), now)).toBe(false)
    expect(session.pastHardCeiling(makeChannel({ lastRekeyAt: now - SESSION_KEY_HARD_CEILING_MS }), now)).toBe(true)
  })

  it('returns true from needsRekeyGate for age or hard ceiling', () => {
    const { session } = harness()
    expect(session.needsRekeyGate(makeChannel())).toBe(false)
    expect(session.needsRekeyGate(agedChannel())).toBe(true)
    expect(session.needsRekeyGate(makeChannel({
      lastRekeyAt: performance.now() - SESSION_KEY_HARD_CEILING_MS - 1,
    }))).toBe(true)
  })

  it('starts a fresh record with the key age now and the rekey ids at FIRST_REKEY_CORRELATION_ID', () => {
    const before = performance.now()
    const ch = makeChannel()
    expect(ch.lastRekeyAt).toBeGreaterThanOrEqual(before)
    expect(ch.nextRekeyId).toBe(FIRST_REKEY_CORRELATION_ID)
    expect(ch.highestStartedId).toBe(0)
    expect(ch.reassembly.size()).toBe(0)
  })
})

describe('ChannelSession send', () => {
  it('posts a single frame for a small plaintext at once', () => {
    const cipher = mockCipher()
    const { session, frames } = harness()
    session.send(makeChannel({ session: cipher }), new Uint8Array([1, 2, 3]), 4, true)
    expect(decodeFrames(frames)).toEqual([
      { id: 4, more: false },
    ])
    expect(cipher.send.encrypt).toHaveBeenCalledOnce()
  })

  it('puts a message of three chunks on the wire as MORE, MORE and final frames in one call', () => {
    const cipher = mockCipher()
    const { session, frames } = harness()
    session.send(makeChannel({ session: cipher }), chunks(3), 7, true)
    expect(decodeFrames(frames)).toEqual([
      { id: 7, more: true },
      { id: 7, more: true },
      { id: 7, more: false },
    ])
    // One encryption for each chunk, in chunk order.
    expect(cipher.send.encrypt.mock.calls.map(([plaintext]) => plaintext.length)).toEqual([MAX_CHUNK_SIZE, MAX_CHUNK_SIZE, 10])
  })

  // A stream sends its follow-ups and its cancel under its own correlation ID.
  // The Worker joins every frame of one ID up to the first frame without MORE,
  // so a frame that went out between two chunks would end the message there.
  it('sends a single frame of the same correlation ID after the last chunk of a chunked message', () => {
    const { session, frames } = harness()
    const ch = makeChannel()
    session.send(ch, chunks(2), 7, true)
    session.send(ch, Uint8Array.of(1, 2, 3), 7, false)

    // The mock cipher encrypts to the plaintext itself, so each ciphertext
    // tells the two messages apart: the chunks end with 10 bytes, and the
    // single frame holds 3.
    expect(decodeMessages(frames).map(message => ({
      id: Number(message.correlationId),
      more: message.flags === ChannelMessageFlags.MORE,
      bytes: message.ciphertext.length,
    }))).toEqual([
      { id: 7, more: true, bytes: MAX_CHUNK_SIZE },
      { id: 7, more: false, bytes: 10 },
      { id: 7, more: false, bytes: 3 },
    ])
  })

  it('sends one frame for an empty message', () => {
    const { session, frames } = harness()
    session.send(makeChannel(), new Uint8Array(0), 9, true)
    expect(decodeFrames(frames)).toEqual([
      { id: 9, more: false },
    ])
  })

  it('marks a starting id live and raises the highest started id, but not for a follow-up', () => {
    const { session } = harness()
    const ch = makeChannel()
    session.send(ch, new Uint8Array(1), 5, true)
    session.send(ch, new Uint8Array(1), 3, true)
    session.send(ch, new Uint8Array(1), 8, false)

    expect([...ch.liveIds]).toEqual([5, 3])
    expect(ch.highestStartedId).toBe(5)
  })

  it('holds a send while a rekey is in flight and sends it after the Ack, in arrival order', async () => {
    const { session, frames } = harness()
    const ch = agedChannel()
    session.send(ch, new Uint8Array(1), 5, true)
    session.send(ch, new Uint8Array(2), 6, true)
    await yieldToEventLoop()

    // Only the RekeyRequest is on the wire; both sends wait.
    expect(frameKinds(frames)).toEqual(['rekeyRequest'])
    expect(ch.held.map(item => item.correlationId)).toEqual([5, 6])

    ch.rekeyWait!(true, 0)
    // A send that arrives after the Ack, while the earlier sends still wait,
    // waits behind them.
    session.send(ch, new Uint8Array(3), 7, true)
    expect(ch.held.map(item => item.correlationId)).toEqual([5, 6, 7])
    await yieldToEventLoop()

    expect(decodeFrames(frames).map(frame => frame.id)).toEqual([FIRST_REKEY_CORRELATION_ID, 5, 6, 7])
    expect(ch.held).toEqual([])
    expect(ch.draining).toBe(false)
  })

  // The Reject leaves the keys, and its backoff keeps the next message from
  // asking for another rekey at once.
  it('sends the held lane after a Reject, under the key that it has, and asks for no second rekey', async () => {
    const cipher = mockCipher()
    const { session, frames } = harness()
    const ch = agedChannel({ session: cipher })
    session.send(ch, new Uint8Array(1), 5, true)
    session.send(ch, new Uint8Array(2), 6, true)
    await yieldToEventLoop()
    expect(frameKinds(frames)).toEqual(['rekeyRequest'])

    ch.rekeyWait!(false, 30_000)
    await yieldToEventLoop()

    expect(decodeFrames(frames).map(frame => frame.id)).toEqual([FIRST_REKEY_CORRELATION_ID, 5, 6])
    expect(cipher.send.rekeyWithSecret).not.toHaveBeenCalled()
    expect(ch.held).toEqual([])
    expect(ch.draining).toBe(false)
  })

  it('drops a held starting send whose id was released, and the later held cancel for that id', async () => {
    const { session, frames } = harness()
    const ch = agedChannel()
    session.send(ch, new Uint8Array(1), 5, true)
    session.send(ch, new Uint8Array(2), 6, true)
    session.release(ch, 5)
    session.send(ch, new Uint8Array(3), 5, false)
    await yieldToEventLoop()

    ch.rekeyWait!(true, 0)
    await yieldToEventLoop()

    expect(decodeFrames(frames).map(frame => frame.id)).toEqual([FIRST_REKEY_CORRELATION_ID, 6])
    expect(ch.abandonedIds.size).toBe(0)
  })

  it('still sends a cancel whose starting send already left', async () => {
    const { session, frames } = harness()
    const ch = makeChannel()
    session.send(ch, new Uint8Array(1), 5, true)
    // The key ages while the stream is open.
    ch.lastRekeyAt = performance.now() - SESSION_KEY_MAX_AGE_MS - 1
    session.release(ch, 5)
    session.send(ch, new Uint8Array(2), 5, false)
    await yieldToEventLoop()

    ch.rekeyWait!(true, 0)
    await yieldToEventLoop()

    expect(decodeFrames(frames).map(frame => frame.id)).toEqual([5, FIRST_REKEY_CORRELATION_ID, 5])
  })

  it('ends the session when a send fails to encrypt', () => {
    const cipher = mockCipher()
    cipher.send.encrypt.mockImplementation(() => {
      throw new Error('noise: nonce overflow (hard limit)')
    })
    const { session, frames, ended } = harness()
    const ch = makeChannel({ session: cipher })

    session.send(ch, new Uint8Array(1), 5, true)

    expect(frames).toEqual([])
    expect(ended).toHaveLength(1)
    expect(ended[0]).toMatchObject({ source: 'transport', message: 'noise: nonce overflow (hard limit)' })
    expect(ch.ended).toBe(true)
  })

  it('ends the session when a held send fails to encrypt', async () => {
    const cipher = mockCipher()
    const { session, frames, ended } = harness()
    const ch = agedChannel({ session: cipher })
    session.send(ch, new Uint8Array(1), 5, true)
    await yieldToEventLoop()
    cipher.send.encrypt.mockImplementation(() => {
      throw new Error('noise: nonce overflow (hard limit)')
    })

    ch.rekeyWait!(true, 0)
    await yieldToEventLoop()

    expect(frameKinds(frames)).toEqual(['rekeyRequest'])
    expect(ended.map(error => error.message)).toEqual(['noise: nonce overflow (hard limit)'])
    expect(ch.held).toEqual([])
  })

  it('posts nothing once the session ended', () => {
    const { session, frames } = harness()
    const ch = makeChannel()
    session.dispose(ch)

    session.send(ch, new Uint8Array(1), 5, true)

    expect(frames).toEqual([])
    expect(ch.liveIds.size).toBe(0)
  })

  it('retires the id in release, and keeps a tombstone of a poisoned id', () => {
    const { session } = harness()
    const ch = makeChannel()
    session.send(ch, new Uint8Array(1), 5, true)
    session.send(ch, new Uint8Array(1), 6, true)
    ch.reassembly.accept(5, new Uint8Array(1), true, () => true)
    ch.reassembly.poison(6)

    session.release(ch, 5)
    session.release(ch, 6)

    expect(ch.liveIds.size).toBe(0)
    expect(ch.reassembly.get(5)).toBeUndefined()
    expect(ch.reassembly.get(6)?.poisoned).toBe(true)
  })
})

describe('ChannelSession ensureReady', () => {
  it('resolves at once when no rekey is due', async () => {
    const { session, frames } = harness()
    await session.ensureReady(makeChannel())
    expect(frames).toEqual([])
  })

  it('ends the session past the hard ceiling, and throws', async () => {
    const { session, ended } = harness()
    const ch = makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_HARD_CEILING_MS - 1 })

    await expect(session.ensureReady(ch)).rejects.toMatchObject({ source: 'transport', message: 'session key past hard ceiling', disconnected: false })

    expect(ended.map(error => error.message)).toEqual(['session key past hard ceiling'])
    expect(ch.ended).toBe(true)
  })

  it('rekeys when the key is past its max age, then resolves', async () => {
    const { session, frames } = harness()
    const ch = agedChannel()
    const before = performance.now()
    const ready = session.ensureReady(ch)
    await yieldToEventLoop()
    expect(frameKinds(frames)).toEqual(['rekeyRequest'])

    ch.rekeyWait!(true, 0)
    await ready

    expect(ch.lastRekeyAt).toBeGreaterThanOrEqual(before)
  })

  it('rejects with the gone error for a session that ended', async () => {
    const { session } = harness()
    const ch = makeChannel()
    session.dispose(ch)
    await expect(session.ensureReady(ch)).rejects.toMatchObject({ source: 'transport', message: 'channel session is gone' })
  })

  // The second caller waits behind the rekey of the first. Its own turn finds
  // the session ended and returns, so only the check after the wait refuses it.
  it('rejects with the gone error when the session ends during the rekey that it waits behind', async () => {
    const { session } = harness()
    const ch = agedChannel()
    const first = expect(session.ensureReady(ch)).rejects.toMatchObject({ source: 'transport', message: 'channel closed' })
    const second = expect(session.ensureReady(ch)).rejects.toMatchObject({ source: 'transport', message: 'channel session is gone' })
    await yieldToEventLoop()

    session.dispose(ch)

    await first
    await second
  })
})

describe('ChannelSession dispose', () => {
  it('destroys both ciphers, zeroes the rekey material, and empties the held lane', async () => {
    const cipher = mockCipher()
    const { session, frames } = harness()
    const ch = agedChannel({ session: cipher })
    session.send(ch, new Uint8Array(1), 5, true)
    await yieldToEventLoop()
    const material = ch.rekeyMaterial!

    session.dispose(ch)
    await yieldToEventLoop()

    expect(cipher.send.destroy).toHaveBeenCalledOnce()
    expect(cipher.receive.destroy).toHaveBeenCalledOnce()
    expect(allZero(material.ePriv)).toBe(true)
    expect(ch.rekeyMaterial).toBeNull()
    expect(ch.held).toEqual([])
    expect(ch.liveIds.size).toBe(0)
    expect(ch.ended).toBe(true)
    // The held send never went out.
    expect(frameKinds(frames)).toEqual(['rekeyRequest'])
  })

  it('rejects the in-flight rekey with "channel closed"', async () => {
    const { session } = harness()
    const ch = agedChannel()
    const rekey = session.ensureRekeyed(ch)
    await yieldToEventLoop()

    session.dispose(ch)

    await expect(rekey).rejects.toMatchObject({ source: 'transport', message: 'channel closed' })
  })

  it('does nothing the second time', () => {
    const cipher = mockCipher()
    const { session } = harness()
    const ch = makeChannel({ session: cipher })
    session.dispose(ch)
    session.dispose(ch)
    expect(cipher.send.destroy).toHaveBeenCalledOnce()
  })
})

describe('ChannelSession handleRekeyOutcome', () => {
  it('rekeys the ciphers on accept', () => {
    const cipher = mockCipher()
    const ch = makeChannel({ session: cipher })
    const wait = vi.fn()
    ch.rekeyWait = wait
    // Use a real ephemeral so the DH derivation (over real X25519) succeeds.
    const eph = generateRekeyEphemeral()
    ch.rekeyMaterial = { ePriv: eph.privateKey, mlkemSS: null }
    const { session } = harness()
    session.handleRekeyOutcome(ch, true, 0, undefined, eph.publicKey)
    // The send direction keeps no grace window. It is expressed structurally
    // now via rekeyWithSecret(..., false) (which zeroes the replaced key rather
    // than retaining it), NOT via a follow-up clearPrev the caller must
    // remember. Pin both halves: send rotates with retainPrev=false, and
    // neither direction calls clearPrev (the production path no longer needs
    // it). A future edit that re-introduces a clearPrev call — or passes the
    // wrong retainPrev — fails here.
    expect(cipher.send.rekeyWithSecret).toHaveBeenCalledWith(expect.any(Uint8Array), null, false)
    expect(cipher.receive.rekeyWithSecret).toHaveBeenCalledWith(expect.any(Uint8Array), null, true)
    expect(cipher.send.clearPrev).not.toHaveBeenCalled()
    expect(cipher.receive.clearPrev).not.toHaveBeenCalled()
    expect(wait).toHaveBeenCalledWith(true, 0)
  })

  it('does nothing without a waiting rekey', () => {
    const { session, ended } = harness()
    // Must not throw.
    session.handleRekeyOutcome(makeChannel(), false, 1000)
    expect(ended).toEqual([])
  })

  it('zeroes the fresh secrets on reject', () => {
    // Pins the F-1 forward-secrecy fix: a RekeyReject must still wipe the
    // in-flight fresh X25519 ephemeral + ML-KEM shared secret, not leave them
    // lingering in the heap until GC. clearRekeySlot can't reach them (it reads
    // the already-nulled field), so the wipe must happen in this function.
    const cipher = mockCipher()
    const ch = makeChannel({ session: cipher })
    const wait = vi.fn()
    ch.rekeyWait = wait
    const ePriv = generateRekeyEphemeral().privateKey.slice() // owned copy we can re-read
    const mlkemSS = new Uint8Array(32).fill(0xAB)
    ch.rekeyMaterial = { ePriv, mlkemSS }
    const { session } = harness()
    session.handleRekeyOutcome(ch, false, 500)
    expect(wait).toHaveBeenCalledWith(false, 500)
    expect(ch.rekeyMaterial).toBeNull()
    expect(allZero(ePriv)).toBe(true)
    expect(allZero(mlkemSS)).toBe(true)
    // Reject must not rotate keys.
    expect(cipher.send.rekeyWithSecret).not.toHaveBeenCalled()
  })

  it('ends the session and zeroes the fresh secrets on an accept with a bad responder key', () => {
    // The attacker-controlled-Ack path: an Ack whose responder ephemeral is the
    // wrong size must end the session AND wipe the fresh secrets.
    const cipher = mockCipher()
    const ch = makeChannel({ session: cipher })
    ch.rekeyWait = vi.fn()
    ch.rekeyRequestId = 9
    // ensureRekeyed installs rekeyAbort (the close/teardown funnel) whenever it
    // arms a waiter; mirror that here so the bad-pub path's rekeyAbort?.() call
    // has a real target. Like the real funnel, it clears the slot, so the
    // dispose that ends the session finds nothing more to abort.
    const rekeyAbort = vi.fn(() => {
      ch.rekeyAbort = null
      ch.rekeyWait = null
      ch.rekeyRequestId = null
    })
    ch.rekeyAbort = rekeyAbort
    const ePriv = generateRekeyEphemeral().privateKey.slice()
    const mlkemSS = new Uint8Array(32).fill(0xCD)
    ch.rekeyMaterial = { ePriv, mlkemSS }
    const { session, ended } = harness()

    session.handleRekeyOutcome(ch, true, 0, 9, new Uint8Array(7)) // wrong-size dh_pub

    expect(ended).toHaveLength(1)
    expect(ended[0]).toMatchObject({
      source: 'transport',
      message: 'rekey ack carried no valid responder ephemeral key',
      disconnected: false,
    })
    // The teardown funnels through rekeyAbort (clearRekeySlot + reject) rather
    // than hand-nulling rekeyWait.
    expect(rekeyAbort).toHaveBeenCalledOnce()
    expect(allZero(ePriv)).toBe(true)
    expect(allZero(mlkemSS)).toBe(true)
    expect(cipher.send.destroy).toHaveBeenCalledOnce()
  })

  it('ignores mismatched correlation ids', () => {
    const { session } = harness()
    const cipher = mockCipher()
    const wait = vi.fn()
    const ch = makeChannel({
      session: cipher,
      rekeyWait: wait,
      rekeyRequestId: 7,
    })
    session.handleRekeyOutcome(ch, true, 0, 99)
    expect(cipher.send.rekeyWithSecret).not.toHaveBeenCalled()
    expect(wait).not.toHaveBeenCalled()
    expect(ch.rekeyWait).toBe(wait)
  })
})

describe('ChannelSession ensureRekeyed', () => {
  it('ends the session past the hard ceiling', async () => {
    const { session, ended } = harness()
    const ch = makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_HARD_CEILING_MS - 1 })
    await expect(session.ensureRekeyed(ch)).rejects.toThrow(/hard ceiling/)
    expect(ended.map(error => error.message)).toEqual(['session key past hard ceiling'])
  })

  it('does nothing when policy does not require a rekey', async () => {
    const { session, frames } = harness()
    await session.ensureRekeyed(makeChannel())
    expect(frames).toEqual([])
  })

  it('sends a RekeyRequest under FIRST_REKEY_CORRELATION_ID and settles on Ack', async () => {
    const { session, frames } = harness()
    const ch = agedChannel()
    const p = session.ensureRekeyed(ch)
    await yieldToEventLoop()
    expect(ch.rekeyWait).not.toBeNull()
    expect(ch.rekeyRequestId).toBe(FIRST_REKEY_CORRELATION_ID)
    expect(decodeFrames(frames)).toEqual([
      { id: FIRST_REKEY_CORRELATION_ID, more: false },
    ])
    ch.rekeyWait!(true, 0)
    await p
    expect(ch.rekeyWait).toBeNull()
    expect(ch.rekeyRequestId).toBeNull()
    expect(ch.rekeyNotBefore).toBe(0)
  })

  it('allocates rekey ids from FIRST_REKEY_CORRELATION_ID and counts up', async () => {
    const { session, frames } = harness()
    const ch = agedChannel()
    const first = session.ensureRekeyed(ch)
    await yieldToEventLoop()
    ch.rekeyWait!(true, 0)
    await first
    ch.lastRekeyAt = performance.now() - SESSION_KEY_MAX_AGE_MS - 1
    const second = session.ensureRekeyed(ch)
    await yieldToEventLoop()
    ch.rekeyWait!(true, 0)
    await second

    expect(decodeFrames(frames).map(frame => frame.id)).toEqual([FIRST_REKEY_CORRELATION_ID, FIRST_REKEY_CORRELATION_ID + 1])
    expect(ch.nextRekeyId).toBe(FIRST_REKEY_CORRELATION_ID + 2)
    expect(Number.isSafeInteger(ch.nextRekeyId)).toBe(true)
  })

  it('applies retry_after_ms backoff on Reject', async () => {
    const { session } = harness()
    const ch = agedChannel()
    const p = session.ensureRekeyed(ch)
    await yieldToEventLoop()
    expect(ch.rekeyWait).not.toBeNull()
    const before = performance.now()
    ch.rekeyWait!(false, 30_000)
    await p
    expect(ch.rekeyNotBefore).toBeGreaterThanOrEqual(before + 30_000 - 5)
    expect(ch.rekeyRequestId).toBeNull()
  })

  it('applies the default backoff on a Reject that gives no retry delay', async () => {
    const { session } = harness()
    const ch = agedChannel()
    const p = session.ensureRekeyed(ch)
    await yieldToEventLoop()
    const before = performance.now()

    ch.rekeyWait!(false, 0)
    await p

    expect(ch.rekeyNotBefore).toBeGreaterThanOrEqual(before + DEFAULT_REJECT_BACKOFF_MS)
    expect(ch.rekeyNotBefore).toBeLessThanOrEqual(performance.now() + DEFAULT_REJECT_BACKOFF_MS)
    expect(session.shouldInitiateRekey(ch)).toBe(false)
  })

  it('ends the session after Reject once past the hard ceiling', async () => {
    const { session, ended } = harness()
    // Age past max age so we initiate; bump to past the hard ceiling while
    // waiting for Reject (mirrors the "slow peer" race).
    const ch = agedChannel()
    const p = session.ensureRekeyed(ch)
    await yieldToEventLoop()
    expect(ch.rekeyWait).not.toBeNull()
    ch.lastRekeyAt = performance.now() - SESSION_KEY_HARD_CEILING_MS
    ch.rekeyWait!(false, 60_000)
    await expect(p).rejects.toThrow(/hard ceiling after rekey reject/)
    expect(ended.map(error => error.message)).toEqual(['session key past hard ceiling after rekey reject'])
  })

  it('does nothing when the session ended', async () => {
    const { session, frames } = harness()
    const ch = agedChannel()
    session.dispose(ch)
    await session.ensureRekeyed(ch)
    expect(frames).toEqual([])
  })

  it('ends the session and clears the slot when the RekeyRequest fails to encrypt', async () => {
    const cipher = mockCipher()
    cipher.send.encrypt.mockImplementation(() => {
      throw new Error('noise: nonce overflow (hard limit)')
    })
    const { session, ended } = harness()
    const ch = agedChannel({ session: cipher })

    await expect(session.ensureRekeyed(ch)).rejects.toMatchObject({ source: 'transport', message: 'noise: nonce overflow (hard limit)' })

    expect(ended.map(error => error.message)).toEqual(['noise: nonce overflow (hard limit)'])
    expect(ch.rekeyWait).toBeNull()
    expect(ch.rekeyRequestId).toBeNull()
    expect(ch.rekeyMaterial).toBeNull()
  })

  it('chains concurrent callers onto one Request RTT', async () => {
    const { session, frames } = harness()
    const ch = agedChannel()
    const a = session.ensureRekeyed(ch)
    const b = session.ensureRekeyed(ch)
    await yieldToEventLoop()
    expect(frames).toHaveLength(1)
    expect(ch.rekeyWait).not.toBeNull()
    ch.rekeyWait!(true, 0)
    await Promise.all([a, b])
    expect(frames).toHaveLength(1)
  })

  it('rejects the in-flight waiter and clears the slot in abortRekey', async () => {
    const { session } = harness()
    const ch = agedChannel()
    const pending = session.ensureRekeyed(ch)
    await yieldToEventLoop()
    expect(ch.rekeyAbort).not.toBeNull()
    session.abortRekey(ch)
    await expect(pending).rejects.toMatchObject({ source: 'transport', message: 'channel closed' })
    expect(ch.rekeyWait).toBeNull()
    expect(ch.rekeyAbort).toBeNull()
    expect(ch.rekeyRequestId).toBeNull()
  })

  it('throws the same ChannelError that it ends the session with', async () => {
    const { session, ended } = harness()
    const ch = makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_HARD_CEILING_MS - 1 })
    const failure = await session.ensureRekeyed(ch).catch((err: unknown) => err)
    expect(failure).toBeInstanceOf(ChannelError)
    expect(failure).toBe(ended[0])
  })
})
