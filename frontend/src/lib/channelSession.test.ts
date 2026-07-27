import type { SessionChannel } from './channelSession'
import type { Session } from './noise'
import { describe, expect, it, vi } from 'vitest'
import { EncryptionMode } from '~/generated/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import {
  ChannelSession,
  SESSION_KEY_HARD_CEILING_MS,
  SESSION_KEY_MAX_AGE_MS,

} from './channelSession'
import { generateRekeyEphemeral } from './noise-hybrid'
import { MAX_CHUNK_SIZE } from './reassembler'

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
    },
    receive: {
      encrypt: noop,
      decrypt,
      needsRekey: () => recvNeeds,
      rekeyWithSecret: vi.fn(),
      clearPrev: vi.fn(),
      nonce: () => 0,
    },
  } satisfies Session
}

function makeChannel(overrides?: Partial<SessionChannel> & { session?: Session }): SessionChannel {
  return {
    channelId: 'ch-1',
    session: overrides?.session ?? mockCipher(),
    maxReassembledMessageSize: 1024,
    nextRequestId: 1,
    state: 'verified',
    lastRekeyAt: 0,
    rekeyNotBefore: 0,
    rekeyWait: null,
    rekeyClear: null,
    rekeyAbort: null,
    rekeyRequestId: null,
    rekeyChain: Promise.resolve(),
    workerMlkemPub: new Uint8Array(0),
    rekeyMaterial: null,
    ...overrides,
  }
}

function emptyDeps() {
  return {
    sendToWire: () => {},
    closeChannel: async () => {},
    onSendFailure: () => {},
  }
}

const keys = {
  x25519PublicKey: new Uint8Array([1]),
  mlkemPublicKey: new Uint8Array([2]),
  slhdsaPublicKey: new Uint8Array([3]),
}

describe('channelSession', () => {
  it('beginHandshake uses classic Noise for CLASSIC mode', () => {
    const message1 = new Uint8Array([9, 9])
    const finished = mockCipher()
    const classicHandshake1 = vi.fn((_rs: Uint8Array) => ({
      message1,
      handshakeState: { tag: 'classic' } as any,
    }))
    const classicHandshake2 = vi.fn((_hs: any, _payload: Uint8Array) => finished)
    const hybrid1 = vi.fn()
    const session = new ChannelSession(emptyDeps(), {
      classicHandshake1,
      classicHandshake2,
      handshake1: hybrid1 as any,
    })

    const pending = session.beginHandshake(EncryptionMode.CLASSIC, keys)
    expect(classicHandshake1).toHaveBeenCalledWith(keys.x25519PublicKey)
    expect(hybrid1).not.toHaveBeenCalled()
    expect(pending.message1).toBe(message1)

    const payload = new Uint8Array([4, 5])
    expect(pending.finish(payload)).toBe(finished)
    expect(classicHandshake2).toHaveBeenCalledWith({ tag: 'classic' }, payload)
  })

  it('beginHandshake uses hybrid Noise for post-quantum mode', () => {
    const message1 = new Uint8Array([8, 8])
    const finished = mockCipher()
    const handshake1 = vi.fn((_x: Uint8Array, _m: Uint8Array) => ({
      message1,
      handshakeState: { tag: 'hybrid' } as any,
    }))
    const handshake2 = vi.fn((_hs: any, _payload: Uint8Array, _slh: Uint8Array) => finished)
    const classic1 = vi.fn()
    const session = new ChannelSession(emptyDeps(), {
      handshake1,
      handshake2,
      classicHandshake1: classic1 as any,
    })

    const pending = session.beginHandshake(EncryptionMode.POST_QUANTUM, keys)
    expect(handshake1).toHaveBeenCalledWith(keys.x25519PublicKey, keys.mlkemPublicKey)
    expect(classic1).not.toHaveBeenCalled()
    expect(pending.message1).toBe(message1)

    const payload = new Uint8Array([6, 7])
    expect(pending.finish(payload)).toBe(finished)
    expect(handshake2).toHaveBeenCalledWith({ tag: 'hybrid' }, payload, keys.slhdsaPublicKey)
  })

  it('beginHandshake finish propagates handshake-2 failures', () => {
    const session = new ChannelSession(emptyDeps(), {
      handshake1: () => ({
        message1: new Uint8Array([1]),
        handshakeState: {} as any,
      }),
      handshake2: () => {
        throw new Error('bad handshake-2')
      },
    })
    const pending = session.beginHandshake(EncryptionMode.POST_QUANTUM, keys)
    expect(() => pending.finish(new Uint8Array([1]))).toThrow('bad handshake-2')
  })

  it('beginHandshake treats UNSPECIFIED like post-quantum (hybrid path)', () => {
    const handshake1 = vi.fn((_x: Uint8Array, _m: Uint8Array) => ({
      message1: new Uint8Array([1]),
      handshakeState: {} as any,
    }))
    const classic1 = vi.fn()
    const session = new ChannelSession(emptyDeps(), {
      handshake1,
      classicHandshake1: classic1 as any,
    })
    session.beginHandshake(EncryptionMode.UNSPECIFIED, keys)
    expect(handshake1).toHaveBeenCalledOnce()
    expect(classic1).not.toHaveBeenCalled()
  })

  it('beginHandshake classic finish propagates handshake-2 failures', () => {
    const session = new ChannelSession(emptyDeps(), {
      classicHandshake1: () => ({
        message1: new Uint8Array([1]),
        handshakeState: {} as any,
      }),
      classicHandshake2: () => {
        throw new Error('bad classic handshake-2')
      },
    })
    const pending = session.beginHandshake(EncryptionMode.CLASSIC, keys)
    expect(() => pending.finish(new Uint8Array([1]))).toThrow('bad classic handshake-2')
  })

  it('decrypt calls through to the receive cipher', () => {
    const cipher = mockCipher()
    const session = new ChannelSession(emptyDeps())
    const ch = makeChannel({ session: cipher })
    const ct = new Uint8Array([10, 11, 12])
    const out = session.decrypt(ch, ct)
    expect(cipher.receive.decrypt).toHaveBeenCalledWith(ct)
    expect(out).toEqual(ct)
  })

  it('decrypt propagates cipher failures so the coordinator can close', () => {
    const cipher = mockCipher()
    ;(cipher.receive.decrypt as ReturnType<typeof vi.fn>).mockImplementation(() => {
      throw new Error('tag mismatch')
    })
    const session = new ChannelSession(emptyDeps())
    expect(() => session.decrypt(makeChannel({ session: cipher }), new Uint8Array([1]))).toThrow('tag mismatch')
  })

  it('shouldInitiateRekey is true on soft nonce or age', () => {
    const session = new ChannelSession(emptyDeps())
    const soft = makeChannel({ session: mockCipher(true, false), lastRekeyAt: performance.now() })
    expect(session.shouldInitiateRekey(soft)).toBe(true)

    const aged = makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_MAX_AGE_MS - 1 })
    expect(session.shouldInitiateRekey(aged)).toBe(true)

    const fresh = makeChannel({ lastRekeyAt: performance.now() })
    expect(session.shouldInitiateRekey(fresh)).toBe(false)
  })

  it('shouldInitiateRekey respects reject backoff unless soft nonce', () => {
    const session = new ChannelSession(emptyDeps())
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

  it('pastHardCeiling tracks absolute age', () => {
    const session = new ChannelSession(emptyDeps())
    const now = performance.now()
    expect(session.pastHardCeiling(makeChannel({ lastRekeyAt: now }), now)).toBe(false)
    expect(session.pastHardCeiling(makeChannel({ lastRekeyAt: now - SESSION_KEY_HARD_CEILING_MS }), now)).toBe(true)
  })

  it('sendEncryptedMessage rejects oversize with client error', () => {
    const session = new ChannelSession(emptyDeps())
    const ch = makeChannel({ maxReassembledMessageSize: 10 })
    expect(() => session.sendEncryptedMessage(ch, new Uint8Array(11), 1)).toThrow(ChannelError)
  })

  it('sendEncryptedMessage sends a single frame for small plaintext', () => {
    const sent: Uint8Array[] = []
    const cipher = mockCipher()
    const session = new ChannelSession({
      sendToWire: buf => sent.push(buf),
      closeChannel: async () => {},
      onSendFailure: () => {},
    })
    const ch = makeChannel({ session: cipher })
    session.sendEncryptedMessage(ch, new Uint8Array([1, 2, 3]), 4)
    expect(sent).toHaveLength(1)
    expect((cipher.send.encrypt as ReturnType<typeof vi.fn>).mock.calls.length).toBe(1)
  })

  it('sendEncryptedMessage chunks large payloads', () => {
    const sent: Uint8Array[] = []
    const cipher = mockCipher()
    const session = new ChannelSession({
      sendToWire: buf => sent.push(buf),
      closeChannel: async () => {},
      onSendFailure: () => {},
    })
    const ch = makeChannel({
      session: cipher,
      maxReassembledMessageSize: MAX_CHUNK_SIZE * 2 + 100,
    })
    const payload = new Uint8Array(MAX_CHUNK_SIZE + 10)
    session.sendEncryptedMessage(ch, payload, 7)
    expect(sent.length).toBe(2)
    expect((cipher.send.encrypt as ReturnType<typeof vi.fn>).mock.calls.length).toBe(2)
  })

  it('handleRekeyOutcome rekeys ciphers on accept', () => {
    const cipher = mockCipher()
    const ch = makeChannel({ session: cipher })
    const wait = vi.fn()
    ch.rekeyWait = wait
    // Use a real ephemeral so the DH derivation (over real X25519) succeeds.
    const eph = generateRekeyEphemeral()
    ch.rekeyMaterial = { ePriv: eph.privateKey, mlkemSS: null }
    const session = new ChannelSession(emptyDeps())
    session.handleRekeyOutcome(ch, true, 0, undefined, eph.publicKey)
    expect((cipher.send.rekeyWithSecret as ReturnType<typeof vi.fn>)).toHaveBeenCalled()
    expect((cipher.receive.rekeyWithSecret as ReturnType<typeof vi.fn>)).toHaveBeenCalled()
    // The send direction keeps no grace window. It is expressed structurally
    // now via rekeyWithSecret(..., false) (which zeroes the replaced key rather
    // than retaining it), NOT via a follow-up clearPrev the caller must
    // remember. Pin both halves: send rotates with retainPrev=false, and
    // neither direction calls clearPrev (the production path no longer needs
    // it). A future edit that re-introduces a clearPrev call — or passes the
    // wrong retainPrev — fails here.
    expect((cipher.send.rekeyWithSecret as ReturnType<typeof vi.fn>)).toHaveBeenCalledWith(expect.any(Uint8Array), null, false)
    expect((cipher.receive.rekeyWithSecret as ReturnType<typeof vi.fn>)).toHaveBeenCalledWith(expect.any(Uint8Array), null, true)
    expect((cipher.send.clearPrev as ReturnType<typeof vi.fn>)).not.toHaveBeenCalled()
    expect((cipher.receive.clearPrev as ReturnType<typeof vi.fn>)).not.toHaveBeenCalled()
    expect(wait).toHaveBeenCalledWith(true, 0)
  })

  it('handleRekeyOutcome without wait is a no-op warn path', () => {
    const session = new ChannelSession(emptyDeps())
    // Must not throw.
    session.handleRekeyOutcome(makeChannel(), false, 1000)
  })

  it('handleRekeyOutcome zeroes the fresh secrets on reject', () => {
    // Pins the F-1 forward-secrecy fix: a RekeyReject must still wipe the
    // in-flight fresh X25519 ephemeral + ML-KEM shared secret, not leave them
    // lingering in the heap until GC. Pre-fix, handleRekeyOutcome nulled
    // ch.rekeyMaterial before the if(accepted) branch, so only the accept-success
    // path filled the bytes — the reject path leaked the very secrets whose
    // freshness is #321's guarantee. clearRekeySlot can't reach them (it reads
    // the already-nulled field), so the wipe must happen in this function.
    const cipher = mockCipher()
    const ch = makeChannel({ session: cipher })
    const wait = vi.fn()
    ch.rekeyWait = wait
    const ePriv = generateRekeyEphemeral().privateKey.slice() // owned copy we can re-read
    const mlkemSS = new Uint8Array(32).fill(0xAB)
    ch.rekeyMaterial = { ePriv, mlkemSS }
    const session = new ChannelSession(emptyDeps())
    session.handleRekeyOutcome(ch, false, 500)
    expect(wait).toHaveBeenCalledWith(false, 500)
    expect(ch.rekeyMaterial).toBeNull()
    // The fresh secrets must be wiped on the reject path.
    expect(Array.from(ePriv).every(b => b === 0)).toBe(true)
    expect(Array.from(mlkemSS).every(b => b === 0)).toBe(true)
    // Reject must not rotate keys.
    expect((cipher.send.rekeyWithSecret as ReturnType<typeof vi.fn>)).not.toHaveBeenCalled()
  })

  it('handleRekeyOutcome zeroes the fresh secrets on accept-with-bad-responder-pub', () => {
    // Pins the F-1 fail-closed branch: an Ack whose responder ephemeral is the
    // wrong size must close the channel AND wipe the fresh secrets — this is
    // the attacker-controlled-Ack path, so leaking here would be the worst
    // instance of the bug.
    const cipher = mockCipher()
    const ch = makeChannel({ session: cipher })
    const wait = vi.fn()
    ch.rekeyWait = wait
    ch.rekeyRequestId = 9
    // ensureRekeyed installs rekeyAbort (the close/teardown funnel) whenever it
    // arms a waiter; mirror that here so the bad-pub path's rekeyAbort?.() call
    // has a real target. The funnel (not a hand-null of rekeyWait) is what
    // clears the timer + slot and rejects the outcome promise.
    const rekeyAbort = vi.fn()
    ch.rekeyAbort = rekeyAbort
    const ePriv = generateRekeyEphemeral().privateKey.slice()
    const mlkemSS = new Uint8Array(32).fill(0xCD)
    ch.rekeyMaterial = { ePriv, mlkemSS }
    const closeChannel = vi.fn(async () => {})
    const session = new ChannelSession({
      sendToWire: () => {},
      closeChannel,
      onSendFailure: () => {},
    })
    session.handleRekeyOutcome(ch, true, 0, 9, new Uint8Array(7)) // wrong-size dh_pub
    expect(closeChannel).toHaveBeenCalledWith('ch-1')
    // The teardown funnels through rekeyAbort (clearRekeySlot + reject) rather
    // than hand-nulling rekeyWait — pinning that the bad-pub path does not
    // bypass the single cleanup funnel.
    expect(rekeyAbort).toHaveBeenCalledTimes(1)
    expect(Array.from(ePriv).every(b => b === 0)).toBe(true)
    expect(Array.from(mlkemSS).every(b => b === 0)).toBe(true)
  })

  it('ensureRekeyed closes past hard ceiling', async () => {
    const closeChannel = vi.fn(async () => {})
    const session = new ChannelSession({
      sendToWire: () => {},
      closeChannel,
      onSendFailure: () => {},
    })
    const ch = makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_HARD_CEILING_MS - 1 })
    await expect(session.ensureRekeyed(ch)).rejects.toThrow(/hard ceiling/)
    expect(closeChannel).toHaveBeenCalledWith('ch-1')
  })

  it('ensureRekeyed is a no-op when policy does not require rekey', async () => {
    const sendToWire = vi.fn()
    const session = new ChannelSession({
      sendToWire,
      closeChannel: async () => {},
      onSendFailure: () => {},
    })
    const ch = makeChannel({ lastRekeyAt: performance.now() })
    await session.ensureRekeyed(ch)
    expect(sendToWire).not.toHaveBeenCalled()
  })

  it('ensureRekeyed sends RekeyRequest and settles on Ack', async () => {
    const sent: Uint8Array[] = []
    const session = new ChannelSession({
      sendToWire: buf => sent.push(buf),
      closeChannel: async () => {},
      onSendFailure: () => {},
    })
    const ch = makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_MAX_AGE_MS - 1 })
    const p = session.ensureRekeyed(ch)
    // Allow the locked path to install rekeyWait.
    await Promise.resolve()
    expect(ch.rekeyWait).not.toBeNull()
    expect(ch.rekeyRequestId).toBe(1)
    expect(sent.length).toBe(1)
    ch.rekeyWait!(true, 0)
    await p
    expect(ch.rekeyWait).toBeNull()
    expect(ch.rekeyRequestId).toBeNull()
    expect(ch.rekeyNotBefore).toBe(0)
  })

  it('ensureRekeyed on Reject applies retry_after_ms backoff', async () => {
    const session = new ChannelSession(emptyDeps())
    const ch = makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_MAX_AGE_MS - 1 })
    const p = session.ensureRekeyed(ch)
    await Promise.resolve()
    expect(ch.rekeyWait).not.toBeNull()
    const before = performance.now()
    ch.rekeyWait!(false, 30_000)
    await p
    expect(ch.rekeyNotBefore).toBeGreaterThanOrEqual(before + 30_000 - 5)
    expect(ch.rekeyRequestId).toBeNull()
  })

  it('ensureRekeyed closes after Reject once past hard ceiling', async () => {
    const closeChannel = vi.fn(async () => {})
    const session = new ChannelSession({
      sendToWire: () => {},
      closeChannel,
      onSendFailure: () => {},
    })
    // Age past max age so we initiate; bump to past the hard ceiling while
    // waiting for Reject (mirrors the coordinator's "slow peer" race).
    const ch = makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_MAX_AGE_MS - 1 })
    const p = session.ensureRekeyed(ch)
    await Promise.resolve()
    expect(ch.rekeyWait).not.toBeNull()
    ch.lastRekeyAt = performance.now() - SESSION_KEY_HARD_CEILING_MS
    ch.rekeyWait!(false, 60_000)
    await expect(p).rejects.toThrow(/hard ceiling after rekey reject/)
    expect(closeChannel).toHaveBeenCalledWith('ch-1')
  })

  it('ensureRekeyed is a no-op when the channel is already closed', async () => {
    const sendToWire = vi.fn()
    const session = new ChannelSession({
      sendToWire,
      closeChannel: async () => {},
      onSendFailure: () => {},
    })
    const ch = makeChannel({
      state: 'closed',
      lastRekeyAt: performance.now() - SESSION_KEY_MAX_AGE_MS - 1,
    })
    await session.ensureRekeyed(ch)
    expect(sendToWire).not.toHaveBeenCalled()
  })

  it('ensureRekeyed reports send failure and clears the wait', async () => {
    const onSendFailure = vi.fn()
    const session = new ChannelSession({
      sendToWire: () => {
        throw new ChannelError('transport', 'ws dead')
      },
      closeChannel: async () => {},
      onSendFailure,
    })
    const ch = makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_MAX_AGE_MS - 1 })
    await expect(session.ensureRekeyed(ch)).rejects.toThrow('ws dead')
    expect(onSendFailure).toHaveBeenCalledOnce()
    expect(ch.rekeyWait).toBeNull()
    expect(ch.rekeyRequestId).toBeNull()
  })

  it('ensureRekeyed chains concurrent callers onto one Request RTT', async () => {
    const sent: Uint8Array[] = []
    const session = new ChannelSession({
      sendToWire: buf => sent.push(buf),
      closeChannel: async () => {},
      onSendFailure: () => {},
    })
    const ch = makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_MAX_AGE_MS - 1 })
    const a = session.ensureRekeyed(ch)
    const b = session.ensureRekeyed(ch)
    await Promise.resolve()
    expect(sent).toHaveLength(1)
    expect(ch.rekeyWait).not.toBeNull()
    ch.rekeyWait!(true, 0)
    await Promise.all([a, b])
    expect(sent).toHaveLength(1)
  })

  it('abortRekey rejects the in-flight waiter and clears the slot', async () => {
    const session = new ChannelSession({
      sendToWire: () => {},
      closeChannel: async () => {},
      onSendFailure: () => {},
    })
    const ch = makeChannel({ lastRekeyAt: performance.now() - SESSION_KEY_MAX_AGE_MS - 1 })
    const pending = session.ensureRekeyed(ch)
    await Promise.resolve()
    expect(ch.rekeyAbort).not.toBeNull()
    session.abortRekey(ch)
    await expect(pending).rejects.toMatchObject({ source: 'transport', message: 'channel closed' })
    expect(ch.rekeyWait).toBeNull()
    expect(ch.rekeyAbort).toBeNull()
    expect(ch.rekeyRequestId).toBeNull()
  })

  it('handleRekeyOutcome ignores mismatched correlation ids', () => {
    const session = new ChannelSession(emptyDeps())
    const cipher = mockCipher()
    const wait = vi.fn()
    const ch = makeChannel({
      session: cipher,
      rekeyWait: wait,
      rekeyRequestId: 7,
    })
    session.handleRekeyOutcome(ch, true, 0, 99)
    expect((cipher.send.rekeyWithSecret as ReturnType<typeof vi.fn>)).not.toHaveBeenCalled()
    expect(wait).not.toHaveBeenCalled()
    expect(ch.rekeyWait).toBe(wait)
  })

  it('needsRekeyGate is true for age or hard ceiling', () => {
    const session = new ChannelSession(emptyDeps())
    expect(session.needsRekeyGate(makeChannel({ lastRekeyAt: performance.now() }))).toBe(false)
    expect(session.needsRekeyGate(makeChannel({
      lastRekeyAt: performance.now() - SESSION_KEY_MAX_AGE_MS - 1,
    }))).toBe(true)
    expect(session.needsRekeyGate(makeChannel({
      lastRekeyAt: performance.now() - SESSION_KEY_HARD_CEILING_MS - 1,
    }))).toBe(true)
  })
})
