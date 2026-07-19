import type { ChannelSocket, ChannelTransport, KeyPinDecision, WorkerKeyBundle } from './channel'
import type { Session } from './noise'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { chacha20poly1305 } from '@noble/ciphers/chacha.js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ChannelMessageSchema,
  EncryptionMode,
  InnerMessageSchema,
  InnerRpcRequestSchema,
  InnerRpcResponseSchema,
  InnerStreamMessageSchema,
} from '~/generated/leapmux/v1/channel_pb'
import { KEY_KEY_PINS, localStorageGet } from './browserStorage'
import {
  ChannelError,
  ChannelManager,
  PING_METHOD,
} from './channel'
import { DEFAULT_MAX_MESSAGE_SIZE, MAX_CHUNK_SIZE, MAX_INCOMPLETE_CHUNKED } from './reassembler'

// ---- Test helpers ----

/** Minimal CipherState for testing (same crypto as the real one). */
class TestCipherState {
  private k: Uint8Array
  private n: number

  constructor(key: Uint8Array) {
    this.k = key
    this.n = 0
  }

  encrypt(plaintext: Uint8Array): Uint8Array {
    const nonceBytes = new Uint8Array(12)
    new DataView(nonceBytes.buffer).setUint32(4, this.n, true)
    const cipher = chacha20poly1305(this.k, nonceBytes, new Uint8Array(0))
    const ct = cipher.encrypt(plaintext)
    this.n++
    return ct
  }

  decrypt(ciphertext: Uint8Array): Uint8Array {
    const nonceBytes = new Uint8Array(12)
    new DataView(nonceBytes.buffer).setUint32(4, this.n, true)
    const cipher = chacha20poly1305(this.k, nonceBytes, new Uint8Array(0))
    const pt = cipher.decrypt(ciphertext)
    this.n++
    return pt
  }

  needsRekey(): boolean {
    return false
  }
}

/** Create a matched pair of cipher states (send/receive mirrors). */
function createTestSession(): { initiator: Session, responder: Session } {
  const key1 = new Uint8Array(32)
  const key2 = new Uint8Array(32)
  key1[0] = 1 // Different keys for send/receive
  key2[0] = 2
  return {
    initiator: { send: new TestCipherState(key1), receive: new TestCipherState(key2) } as unknown as Session,
    responder: { send: new TestCipherState(key2), receive: new TestCipherState(key1) } as unknown as Session,
  }
}

/** Encode a ChannelMessage into the wire format (4-byte length prefix + protobuf). */
function encodeWireMessage(channelId: string, ciphertext: Uint8Array, opts?: { close?: boolean, id?: number }): ArrayBuffer {
  const msg = create(ChannelMessageSchema, {
    protocolVersion: 1,
    channelId,
    ciphertext,
    flags: opts?.close ? 2 : 0,
    correlationId: BigInt(opts?.id ?? 0),
  })
  const data = toBinary(ChannelMessageSchema, msg)
  const buf = new Uint8Array(4 + data.length)
  new DataView(buf.buffer).setUint32(0, data.length)
  buf.set(data, 4)
  return buf.buffer
}

/**
 * Encode a wire message with an arbitrary uint64 correlation id, including ones
 * outside the range this client's own allocator can produce.
 */
function encodeWireMessageWithBigIntId(channelId: string, ciphertext: Uint8Array, id: bigint): ArrayBuffer {
  const msg = create(ChannelMessageSchema, { protocolVersion: 1, channelId, ciphertext, correlationId: id })
  const data = toBinary(ChannelMessageSchema, msg)
  const buf = new Uint8Array(4 + data.length)
  new DataView(buf.buffer).setUint32(0, data.length)
  buf.set(data, 4)
  return buf.buffer
}

/** Encode a close notification. */
function encodeCloseMessage(channelId: string): ArrayBuffer {
  return encodeWireMessage(channelId, new Uint8Array(), { close: true })
}

/** Parse a sent wire-format buffer back into a ChannelMessage. */
function decodeWireMessage(buf: ArrayBuffer) {
  const arr = new Uint8Array(buf)
  const length = new DataView(arr.buffer, arr.byteOffset).getUint32(0)
  return fromBinary(ChannelMessageSchema, arr.slice(4, 4 + length))
}

/**
 * A channel's private per-request registries.
 *
 * Tests assert on their contents because a leak here has no public surface: a stranded
 * pending entry, an unreachable stream listener or an abandoned reassembly buffer just
 * sits in the map holding memory and a cap slot, and every public method keeps
 * answering exactly as it did before. The map IS the observable.
 */
interface ChannelInternals {
  session: { send: { encrypt: (pt: Uint8Array) => Uint8Array } }
  pendingRequests: Map<number, unknown>
  streamListeners: Map<number, unknown>
  // The Reassembler owns the buffers; tests reach through its get/size/liveCount.
  reassembly: {
    get: (id: number) => { parts: Uint8Array[], total: number, poisoned: boolean } | undefined
    size: () => number
    liveCount: () => number
  }
  state: 'opening' | 'verified' | 'closed'
}

function channelInternals(cm: ChannelManager, channelId: string): ChannelInternals {
  const ch = (cm as unknown as { channels: Map<string, ChannelInternals> }).channels.get(channelId)
  if (!ch) {
    throw new Error(`no channel ${channelId} on the manager`)
  }
  return ch
}

// ---- Mock WebSocket ----

type EventCallback = (...args: any[]) => void

class MockWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  readyState = MockWebSocket.CONNECTING
  binaryType = 'arraybuffer'

  private listeners = new Map<string, Set<EventCallback>>()
  sent: ArrayBuffer[] = []

  private onceListeners = new Set<EventCallback>()

  addEventListener(type: string, listener: EventCallback, opts?: { once?: boolean }) {
    if (!this.listeners.has(type)) {
      this.listeners.set(type, new Set())
    }
    this.listeners.get(type)!.add(listener)
    if (opts?.once) {
      this.onceListeners.add(listener)
    }
  }

  removeEventListener(type: string, listener: EventCallback) {
    this.listeners.get(type)?.delete(listener)
    this.onceListeners.delete(listener)
  }

  send(data: ArrayBuffer | Uint8Array) {
    if (data instanceof Uint8Array) {
      this.sent.push(data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) as ArrayBuffer)
    }
    else {
      this.sent.push(data)
    }
  }

  close(_code?: number, _reason?: string) {
    this.readyState = MockWebSocket.CLOSED
    this.emit('close')
  }

  // -- Test helpers --

  simulateOpen() {
    this.readyState = MockWebSocket.OPEN
    this.emit('open')
  }

  simulateMessage(data: ArrayBuffer) {
    this.emit('message', { data } as MessageEvent)
  }

  simulateClose() {
    this.readyState = MockWebSocket.CLOSED
    this.emit('close')
  }

  simulateError() {
    this.emit('error')
  }

  private emit(type: string, event?: any) {
    const handlers = this.listeners.get(type)
    if (handlers) {
      for (const h of [...handlers]) {
        h(event)
        if (this.onceListeners.has(h)) {
          handlers.delete(h)
          this.onceListeners.delete(h)
        }
      }
    }
  }
}

// ---- Mock Transport ----

const sessions = new Map<string, { initiator: Session, responder: Session }>()

function createMockTransport(mockWs: MockWebSocket): ChannelTransport {
  return {
    async getWorkerHandshakeParams(_workerId: string): Promise<{ keys: WorkerKeyBundle, encryptionMode: EncryptionMode }> {
      // Return dummy keys. The real handshake is bypassed
      // since we mock initiatorHandshake1/2.
      return {
        keys: {
          x25519PublicKey: new Uint8Array(32),
          mlkemPublicKey: new Uint8Array(1568),
          slhdsaPublicKey: new Uint8Array(64),
        },
        encryptionMode: EncryptionMode.POST_QUANTUM,
      }
    },
    async openChannel(_workerId: string, _handshakePayload: Uint8Array) {
      const channelId = `ch-${Math.random().toString(36).slice(2, 8)}`
      const pair = createTestSession()
      sessions.set(channelId, pair)
      // Return the handshake payload that initiatorHandshake2 expects.
      // Since we mock the handshake functions, the actual bytes don't matter.
      // userId is the Hub-authenticated identity, distinct from getUserId() so
      // tests can assert the claim uses the Hub value, not the local one.
      return { channelId, handshakePayload: new Uint8Array(49904), userId: 'hub-authenticated-user' }
    },
    async closeChannel(_channelId: string) {},
    createWebSocket(): ChannelSocket {
      return mockWs
    },
    async confirmKeyPin(_workerId: string, _expectedFingerprint: string, _actualFingerprint: string): Promise<KeyPinDecision> {
      return 'accept'
    },
  }
}

// ---- Mock handshake functions (injected via ChannelManager DI) ----

function mockHandshake1(_remoteX25519Pub: Uint8Array, _remoteMlkemPub: Uint8Array) {
  return {
    handshakeState: {} as any,
    message1: new Uint8Array(1616),
  }
}

function mockHandshake2(_state: any, _message2: Uint8Array, _remoteSlhdsaPub: Uint8Array) {
  const entries = [...sessions.entries()]
  const lastEntry = entries.at(-1)
  if (!lastEntry)
    throw new Error('No session registered')
  return lastEntry[1].initiator
}

// ---- Tests ----

describe('channelManager', () => {
  let mockWs: MockWebSocket
  let mgr: ChannelManager

  beforeEach(() => {
    sessions.clear()
    mockWs = new MockWebSocket()
    mgr = new ChannelManager(createMockTransport(mockWs), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
    })
  })

  afterEach(() => {
    mgr.closeAll()
  })

  /**
   * Flush pending microtasks so async code that chains multiple
   * resolved-promise awaits (like openChannel) progresses.
   */
  async function flushMicrotasks() {
    for (let i = 0; i < 10; i++) {
      await Promise.resolve()
    }
  }

  /**
   * The correlation id of the FIRST request a test issues after openTestChannel:
   * the open itself consumes id 1 on each channel for its session-verifying Ping.
   * Naming it once keeps the coupling in one place -- if the open ever round-trips
   * another RPC, only this changes.
   */
  const FIRST_TEST_REQUEST_ID = 2

  /**
   * Answer the open-time Ping. openChannel round-trips a no-op Ping to prove the
   * E2EE session decrypts in both directions before it resolves, so a test worker
   * must reply to it or the open never completes. Decrypts the ping (advancing the
   * responder's receive nonce) and replies on its own correlation id.
   */
  function simulatePingAccept(ws: MockWebSocket = mockWs, sessionMap = sessions) {
    const sentMsg = decodeWireMessage(ws.sent.at(-1)!)
    const pair = sessionMap.get(sentMsg.channelId)!
    const inner = fromBinary(InnerMessageSchema, pair.responder.receive.decrypt(sentMsg.ciphertext))
    expect(inner.kind.case).toBe('request')
    const resp = create(InnerRpcResponseSchema, { isError: false })
    const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
    const ciphertext = pair.responder.send.encrypt(toBinary(InnerMessageSchema, envelope))
    ws.simulateMessage(encodeWireMessage(sentMsg.channelId, ciphertext, { id: Number(sentMsg.correlationId) }))
  }

  async function openTestChannel(workerId = 'w1'): Promise<string> {
    const openPromise = mgr.openChannel(workerId)
    // Flush microtasks so openChannel progresses through its awaits
    // and ensureWebSocket() registers the 'open' listener.
    await flushMicrotasks()
    mockWs.simulateOpen()
    await flushMicrotasks()
    // The open completes once the worker answers the session-verifying Ping.
    simulatePingAccept()
    return openPromise
  }

  function sendResponseFromWorker(channelId: string, requestId: number, payload: Uint8Array) {
    const pair = sessions.get(channelId)!
    const resp = create(InnerRpcResponseSchema, {
      payload,
      isError: false,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'response', value: resp },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  function sendErrorResponseFromWorker(channelId: string, requestId: number, errorMessage: string) {
    const pair = sessions.get(channelId)!
    const resp = create(InnerRpcResponseSchema, {
      isError: true,
      errorMessage,
      errorCode: 2,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'response', value: resp },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  function sendStreamMessageFromWorker(channelId: string, requestId: number, payload: Uint8Array) {
    const pair = sessions.get(channelId)!
    const msg = create(InnerStreamMessageSchema, {
      payload,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'stream', value: msg },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  function sendStreamEndFromWorker(channelId: string, requestId: number) {
    const pair = sessions.get(channelId)!
    const msg = create(InnerStreamMessageSchema, {
      end: true,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'stream', value: msg },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  function sendStreamErrorFromWorker(channelId: string, requestId: number, errorMessage: string) {
    const pair = sessions.get(channelId)!
    const msg = create(InnerStreamMessageSchema, {
      isError: true,
      errorMessage,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'stream', value: msg },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  describe('openChannel', () => {
    it('should open a channel and return the channel ID', async () => {
      const channelId = await openTestChannel('w1')
      expect(channelId).toBeTruthy()
      expect(mgr.isOpen(channelId)).toBe(true)
      expect(mgr.getWorkerId(channelId)).toBe('w1')
    })

    it('should reuse the WebSocket for multiple channels', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2 = await openTestChannel('w2')
      expect(ch1).not.toBe(ch2)
      expect(mgr.isOpen(ch1)).toBe(true)
      expect(mgr.isOpen(ch2)).toBe(true)
    })

    // A failed open must tell the Hub to drop the channel it already registered.
    //
    // transport.openChannel has returned by the time the Ping runs, so the Hub holds a
    // registered channel and the Worker a live Noise session. Without a rollback, a
    // retry loop against a flaky relay strands one of each per attempt -- consuming
    // the Worker's per-channel caps -- until the credential is revoked or the process
    // restarts. The Go client of this protocol rolls back at exactly this boundary
    // (backend/tunnel/channel.go's rollbackRegisteredChannel).
    it('tells the Hub to close the channel when the session ping fails', async () => {
      const brokenWs = new MockWebSocket()
      const base = createMockTransport(brokenWs)
      const closed: string[] = []
      const transport: ChannelTransport = {
        ...base,
        async closeChannel(channelId) {
          closed.push(channelId)
        },
      }
      const brokenMgr = new ChannelManager(transport, {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        rpcTimeoutFn: () => 20,
      })
      try {
        const openPromise = brokenMgr.openChannel('w1')
        await flushMicrotasks()
        brokenWs.simulateOpen()
        await flushMicrotasks()
        // The worker never answers the ping.
        await expect(openPromise).rejects.toThrow()
        expect(closed).toHaveLength(1)
      }
      finally {
        brokenMgr.closeAll()
      }
    })

    // Same for the empty-userId rejection: it also fires AFTER openChannel returned.
    it('tells the Hub to close the channel when it omits the authenticated user id', async () => {
      const omittedWs = new MockWebSocket()
      const base = createMockTransport(omittedWs)
      const closed: string[] = []
      const transport: ChannelTransport = {
        ...base,
        async openChannel(workerId, handshakePayload) {
          const r = await base.openChannel(workerId, handshakePayload)
          return { ...r, userId: '' }
        },
        async closeChannel(channelId) {
          closed.push(channelId)
        },
      }
      const omittedMgr = new ChannelManager(transport, { handshake1: mockHandshake1, handshake2: mockHandshake2 })
      try {
        await expect(omittedMgr.openChannel('w1')).rejects.toThrow(/empty authenticated user id/)
        expect(closed).toHaveLength(1)
      }
      finally {
        omittedMgr.closeAll()
      }
    })

    // Handshake-2 verification also runs AFTER the Hub registered the channel, so
    // its failure must roll the registration back exactly like the ping and
    // identity failures: a forged or corrupted handshake-2 (wrong length, bad AEAD
    // tag, invalid SLH-DSA signature) otherwise strands a Hub-registered channel
    // and a live Worker session per retry against a misbehaving worker. The Go
    // client covers the same step under its rollback defer
    // (backend/tunnel/channel.go's handshaker.finish).
    it('tells the Hub to close the channel when handshake-2 verification fails', async () => {
      const ws = new MockWebSocket()
      const base = createMockTransport(ws)
      const closed: string[] = []
      const transport: ChannelTransport = {
        ...base,
        async closeChannel(channelId) {
          closed.push(channelId)
        },
      }
      const failingMgr = new ChannelManager(transport, {
        handshake1: mockHandshake1,
        handshake2: () => {
          throw new Error('handshake message 2 failed to verify')
        },
      })
      try {
        await expect(failingMgr.openChannel('w1')).rejects.toThrow(/failed to verify/)
        expect(closed).toHaveLength(1)
        expect(failingMgr.hasOpenChannel('w1')).toBe(false)
      }
      finally {
        failingMgr.closeAll()
      }
    })

    // A throw AFTER the channel entered the pool as verified (today only a
    // hypothetical commitPin failure -- browserStorage swallows write errors -- but
    // the exit must stay safe if that ever changes) must evict the channel as well
    // as roll the Hub registration back: a verified ghost left in the pool would be
    // served by getOrOpenChannel for up to an hour while every RPC on it times out.
    it('evicts the pooled channel when a post-verification step throws', async () => {
      const ws = new MockWebSocket()
      const base = createMockTransport(ws)
      const closed: string[] = []
      const transport: ChannelTransport = {
        ...base,
        async closeChannel(channelId) {
          closed.push(channelId)
        },
      }
      const throwingMgr = new ChannelManager(transport, {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
      })
      vi.spyOn(
        throwingMgr as unknown as { resolveKeyPin: (workerId: string, keys: WorkerKeyBundle) => Promise<() => void> },
        'resolveKeyPin',
      ).mockResolvedValue(() => {
        throw new Error('pin store rejected the write')
      })
      try {
        const openPromise = throwingMgr.openChannel('w1')
        await flushMicrotasks()
        ws.simulateOpen()
        await flushMicrotasks()
        simulatePingAccept(ws)
        await expect(openPromise).rejects.toThrow(/pin store rejected the write/)
        // Not a ghost: the pool must not serve it, and the Hub was told to drop it.
        expect(throwingMgr.hasOpenChannel('w1')).toBe(false)
        expect(closed).toHaveLength(1)
      }
      finally {
        throwingMgr.closeAll()
      }
    })

    // The open must not hand back a channel whose session does not actually work.
    // Noise_NK's handshake only proves THIS side can encrypt to the worker's
    // static key -- nothing in it proves the worker decrypts, or that its replies
    // decrypt back. Without the open-time Ping round trip, a session broken in
    // either direction opened "successfully" and failed on the caller's first real
    // call, and getOrOpenChannel would have cached the broken channel and served it
    // to every later caller.
    it('rejects the open when the session cannot round-trip a ping', async () => {
      const brokenWs = new MockWebSocket()
      const brokenMgr = new ChannelManager(createMockTransport(brokenWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        rpcTimeoutFn: () => 20,
      })
      try {
        const openPromise = brokenMgr.openChannel('w1')
        await flushMicrotasks()
        brokenWs.simulateOpen()
        await flushMicrotasks()
        // The worker never answers the ping: the session is dead in at least one
        // direction. The open must fail rather than cache a broken channel.
        await expect(openPromise).rejects.toThrow()
        expect(brokenMgr.isOpen('ch-1')).toBe(false)
      }
      finally {
        brokenMgr.closeAll()
      }
    })

    // A Hub response with no authenticated user id must be REJECTED, not quietly
    // replaced with a locally-asserted identity. Falling back re-opened the exact
    // hole the Hub-authenticated claim closes: a stale local auth store (an
    // account or impersonation switch) could self-assert an identity the Worker
    // would reject. The Go client of this protocol rejects at the same boundary.
    it('rejects the open when the Hub omits the authenticated user id', async () => {
      const omittedWs = new MockWebSocket()
      const base = createMockTransport(omittedWs)
      const transport: ChannelTransport = {
        ...base,
        async openChannel(workerId, handshakePayload) {
          const r = await base.openChannel(workerId, handshakePayload)
          return { ...r, userId: '' } // Hub omitted the identity
        },
      }
      const omittedMgr = new ChannelManager(transport, { handshake1: mockHandshake1, handshake2: mockHandshake2 })
      try {
        await expect(omittedMgr.openChannel('w1')).rejects.toThrow(/empty authenticated user id/)
        // No claim may be asserted at all -- not a local id, not an empty one:
        // the open is abandoned before anything reaches the wire.
        expect(omittedWs.sent).toHaveLength(0)
      }
      finally {
        omittedMgr.closeAll()
      }
    })

    // A Hub answer that disagrees with who this page thinks it is must FAIL the
    // open, not be taken silently.
    //
    // Validating the identity and then discarding it leaves the two free to
    // diverge: a tab rendered as A whose shared cookie jar has since been
    // re-authenticated as B (a logout/login in another tab, an impersonation
    // switch) opens a channel the Hub authenticates as B, and A's UI then drives
    // B's session on every worker B can reach. The Hub stays authoritative --
    // nothing here overrides it -- the open just refuses to proceed on a
    // disagreement the page cannot otherwise see.
    it('rejects the open when the Hub authenticates a different user than expected', async () => {
      const divergedWs = new MockWebSocket()
      const mgr = new ChannelManager(createMockTransport(divergedWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        // The mock Hub authenticates every open as 'hub-authenticated-user'.
        expectedUserId: () => 'stale-user-a',
      })
      try {
        await expect(mgr.openChannel('w1')).rejects.toThrow(/authenticated this channel as hub-authenticated-user, not the expected stale-user-a/)
        // Abandoned before anything reaches the wire.
        expect(divergedWs.sent).toHaveLength(0)
      }
      finally {
        mgr.closeAll()
      }
    })

    // An EMPTY-STRING expected identity is a degenerate/corrupt id, NOT "no
    // expectation yet" (undefined). It disagrees with the Hub's real answer, so the
    // open must be refused: treating '' as "no opinion" (the falsy `!!expected` trap)
    // would silently serve a channel bound to a different, non-empty user.
    it('rejects the open when the expected identity is a degenerate empty string', async () => {
      const emptyWs = new MockWebSocket()
      const mgr = new ChannelManager(createMockTransport(emptyWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        expectedUserId: () => '',
      })
      try {
        await expect(mgr.openChannel('w1')).rejects.toThrow(/authenticated this channel as hub-authenticated-user/)
        expect(emptyWs.sent).toHaveLength(0)
      }
      finally {
        mgr.closeAll()
      }
    })

    // A page with no expectation yet (auth still resolving) must not be blocked:
    // undefined means "no opinion", which is different from "expects nobody".
    it('opens normally when the page has no expected identity yet', async () => {
      sessions.clear()
      const mgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        expectedUserId: () => undefined,
      })
      try {
        const openPromise = mgr.openChannel('w1')
        await flushMicrotasks()
        mockWs.simulateOpen()
        await flushMicrotasks()
        simulatePingAccept()
        await expect(openPromise).resolves.toBeTruthy()
      }
      finally {
        mgr.closeAll()
      }
    })

    // The matching case must proceed — the check only fires on a real disagreement.
    it('opens when the Hub agrees with the expected identity', async () => {
      sessions.clear()
      const mgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        expectedUserId: () => 'hub-authenticated-user',
      })
      try {
        const openPromise = mgr.openChannel('w1')
        await flushMicrotasks()
        mockWs.simulateOpen()
        await flushMicrotasks()
        simulatePingAccept()
        await expect(openPromise).resolves.toBeTruthy()
      }
      finally {
        mgr.closeAll()
      }
    })
  })

  describe('closeChannel', () => {
    it('should mark channel as closed', async () => {
      const channelId = await openTestChannel('w1')
      await mgr.closeChannel(channelId)
      expect(mgr.isOpen(channelId)).toBe(false)
    })

    it('should reject pending requests on close', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array())
      await mgr.closeChannel(channelId)
      await expect(callPromise).rejects.toThrow('channel closed')
    })

    it('should end active streams on close', async () => {
      const channelId = await openTestChannel('w1')
      const endFn = vi.fn()
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onEnd(endFn)

      await mgr.closeChannel(channelId)
      expect(endFn).toHaveBeenCalled()
    })

    it('should be idempotent', async () => {
      const channelId = await openTestChannel('w1')
      await mgr.closeChannel(channelId)
      await mgr.closeChannel(channelId) // Should not throw.
    })

    // A teardown that races the open must REJECT the pending openChannel, not hang.
    //
    // These three cases were covered while the open awaited a UserIdClaim; the open
    // now awaits a Ping instead, so the same three teardowns must settle the Ping's
    // pendingRequests entry rather than the claim's. The hang they guard against is
    // silent and total: getOrOpenChannel keeps the unresolved promise in its dedup
    // map, so every later caller for that worker awaits it forever with no error and
    // no timeout.
    it('rejects the pending openChannel when closeChannel races the open', async () => {
      const openPromise = mgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()
      // The Ping is now in flight; the worker has not answered it.
      const channelId = decodeWireMessage(mockWs.sent.at(-1)!).channelId
      await mgr.closeChannel(channelId)
      await expect(openPromise).rejects.toThrow(ChannelError)
      await expect(openPromise).rejects.toThrow('channel closed')
    })

    it('rejects the pending openChannel when the server sends CLOSE during the open', async () => {
      const openPromise = mgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()
      const channelId = decodeWireMessage(mockWs.sent.at(-1)!).channelId
      mockWs.simulateMessage(encodeCloseMessage(channelId))
      await expect(openPromise).rejects.toThrow(ChannelError)
      await expect(openPromise).rejects.toThrow('channel closed by server')
    })

    it('rejects the pending openChannel when the WebSocket closes during the open', async () => {
      const openPromise = mgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()
      mockWs.simulateClose()
      await expect(openPromise).rejects.toThrow(ChannelError)
      await expect(openPromise).rejects.toThrow('channel disconnected')
    })
  })

  describe('call', () => {
    // Dropping a message must not desync the session.
    //
    // correlation_id is uint64 on the wire but a plain number in this client, so the
    // decode boundary refuses an id past the exact-integer range rather than rounding
    // it onto some other request's handler. That refusal is defensive -- a rounded id
    // is >= 2^53 and could only collide with an allocated id after ~450,000 years of
    // saturated traffic -- but WHERE it happens is not: Noise nonces are implicit and
    // sequential, so returning before the decrypt leaves this side's receive nonce
    // behind the peer's send nonce and every later message fails to decrypt.
    //
    // That is what this pins: the second half (a normal response still routes after
    // the drop) fails if the check is hoisted above the decrypt.
    it('stays usable after dropping a message with an out-of-range correlation id', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'TestMethod', new Uint8Array())
      const pair = sessions.get(channelId)!

      // 2^53 + 1 is not exactly representable and rounds DOWN onto 2^53, so a naive
      // Number() conversion would hand it to a live handler.
      const unsafeId = BigInt(Number.MAX_SAFE_INTEGER) + 1n
      const resp = create(InnerRpcResponseSchema, { payload: new Uint8Array([9]) })
      const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
      const ct = pair.responder.send.encrypt(toBinary(InnerMessageSchema, envelope))
      mockWs.simulateMessage(encodeWireMessageWithBigIntId(channelId, ct, unsafeId))

      // The pending call is untouched: not resolved, not rejected.
      const settled = await Promise.race([
        callPromise.then(() => 'settled' as const, () => 'settled' as const),
        new Promise<'pending'>(resolve => setTimeout(resolve, 20, 'pending')),
      ])
      expect(settled).toBe('pending')

      // ...and the real response still routes.
      sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([4, 5, 6]))
      await expect(callPromise).resolves.toMatchObject({ payload: new Uint8Array([4, 5, 6]) })
    })

    it('should send a request and receive a response', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'TestMethod', new Uint8Array([1, 2, 3]))

      const requestSentIndex = mockWs.sent.length - 1

      // Decrypt and verify the sent message.
      const sentMsg = decodeWireMessage(mockWs.sent[requestSentIndex])
      expect(sentMsg.channelId).toBe(channelId)
      const pair = sessions.get(channelId)!
      const sentPlaintext = pair.responder.receive.decrypt(sentMsg.ciphertext)
      const sentEnvelope = fromBinary(InnerMessageSchema, sentPlaintext)
      expect(sentEnvelope.kind.case).toBe('request')
      const sentReq = fromBinary(InnerRpcRequestSchema, toBinary(InnerRpcRequestSchema, sentEnvelope.kind.value as any))
      expect(sentReq.method).toBe('TestMethod')
      expect(sentReq.payload).toEqual(new Uint8Array([1, 2, 3]))
      expect(Number(sentMsg.correlationId)).toBe(FIRST_TEST_REQUEST_ID)

      // Send a response from the worker.
      sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([4, 5, 6]))

      const resp = await callPromise
      expect(resp.payload).toEqual(new Uint8Array([4, 5, 6]))
    })

    it('should reject on error response', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'TestMethod', new Uint8Array())

      sendErrorResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, 'something went wrong')

      await expect(callPromise).rejects.toThrow('something went wrong')
    })

    it('should reject if channel is not open', async () => {
      await expect(mgr.call('nonexistent', 'Test', new Uint8Array())).rejects.toThrow('channel not open')
    })

    it('should reject if channel is closed', async () => {
      const channelId = await openTestChannel('w1')
      await mgr.closeChannel(channelId)
      await expect(mgr.call(channelId, 'Test', new Uint8Array())).rejects.toThrow('channel not open')
    })

    it('rejects promptly when the socket is not OPEN instead of hanging until the RPC timeout', async () => {
      const channelId = await openTestChannel('w1')
      // The socket goes to CLOSED WITHOUT its close event draining the channel
      // (a stale/superseded socket the current-socket fence dropped): the channel
      // is still 'verified', so a call reaches the send. sendChannelMessage must
      // THROW here rather than log-and-return, so call()'s catch unregisters and
      // rejects fast -- otherwise the request sits in pendingRequests until the
      // ~15s timeout. No fake timers are advanced, so this only resolves if the
      // rejection is immediate.
      mockWs.readyState = MockWebSocket.CLOSED
      await expect(mgr.call(channelId, 'Test', new Uint8Array([1]))).rejects.toThrow(/WebSocket not open/)
    })

    it('should handle multiple concurrent calls', async () => {
      const channelId = await openTestChannel('w1')

      const call1 = mgr.call(channelId, 'Method1', new Uint8Array([1]))
      const call2 = mgr.call(channelId, 'Method2', new Uint8Array([2]))

      // Respond in reverse order.
      sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID + 1, new Uint8Array([20]))
      sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([10]))

      const [resp1, resp2] = await Promise.all([call1, call2])
      expect(resp1.payload).toEqual(new Uint8Array([10]))
      expect(resp2.payload).toEqual(new Uint8Array([20]))
    })

    it('should timeout after default rpcTimeout (15s)', async () => {
      vi.useFakeTimers()
      try {
        const channelId = await openTestChannel('w1')
        const callPromise = mgr.call(channelId, 'SlowMethod', new Uint8Array())

        vi.advanceTimersByTime(15_000)

        await expect(callPromise).rejects.toThrow('timed out after 15s')
      }
      finally {
        vi.useRealTimers()
      }
    })

    it('should honor a per-call timeout override', async () => {
      vi.useFakeTimers()
      try {
        const channelId = await openTestChannel('w1')
        const callPromise = mgr.call(channelId, 'SlowMethod', new Uint8Array(), 40_000)

        vi.advanceTimersByTime(39_999)
        await Promise.resolve()

        vi.advanceTimersByTime(1)
        await expect(callPromise).rejects.toThrow('timed out after 40s')
      }
      finally {
        vi.useRealTimers()
      }
    })

    it('rejects immediately when an already-aborted signal is passed', async () => {
      const channelId = await openTestChannel('w1')
      const controller = new AbortController()
      controller.abort(new Error('pre-aborted by caller'))
      const callPromise = mgr.call(channelId, 'TestMethod', new Uint8Array(), undefined, controller.signal)
      await expect(callPromise).rejects.toThrow('pre-aborted by caller')
    })

    it('rejects the pending promise when the signal aborts mid-flight', async () => {
      const channelId = await openTestChannel('w1')
      const controller = new AbortController()
      const callPromise = mgr.call(channelId, 'TestMethod', new Uint8Array(), undefined, controller.signal)
      controller.abort(new Error('caller dismissed the dialog'))
      await expect(callPromise).rejects.toThrow('caller dismissed the dialog')
    })

    it('drops the pending entry on abort so a late InnerRpcResponse is harmless', async () => {
      const channelId = await openTestChannel('w1')
      const controller = new AbortController()
      const callPromise = mgr.call(channelId, 'TestMethod', new Uint8Array(), undefined, controller.signal)
      controller.abort(new Error('aborted'))
      await expect(callPromise).rejects.toThrow('aborted')
      // A late worker response for the same correlationId must NOT
      // throw, double-resolve, or surface as an unhandled rejection.
      // The pendingRequest entry was deleted at abort time, so the
      // dispatcher quietly drops the message.
      sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([7, 8, 9]))
      // No assertion needed beyond "doesn't crash" — vitest fails
      // the test on unhandled rejections from the now-detached
      // promise, so the absence of those failures is the signal.
    })

    it('clears the timeout timer when aborted so it cannot fire later and double-reject', async () => {
      vi.useFakeTimers()
      try {
        const channelId = await openTestChannel('w1')
        const controller = new AbortController()
        const callPromise = mgr.call(channelId, 'TestMethod', new Uint8Array(), undefined, controller.signal)
        controller.abort(new Error('aborted'))
        await expect(callPromise).rejects.toThrow('aborted')
        // Advance past the default timeout to prove the timer was
        // cleared — without cleanup, vitest's unhandled-rejection
        // detector would fire when the orphan timer rejected a
        // settled promise.
        vi.advanceTimersByTime(20_000)
      }
      finally {
        vi.useRealTimers()
      }
    })
  })

  describe('stream', () => {
    it('should receive stream messages', async () => {
      const channelId = await openTestChannel('w1')
      const messages: Uint8Array[] = []
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onMessage((msg) => {
        messages.push(msg.payload)
      })

      sendStreamMessageFromWorker(channelId, handle.requestId, new Uint8Array([1]))
      sendStreamMessageFromWorker(channelId, handle.requestId, new Uint8Array([2]))

      expect(messages).toHaveLength(2)
      expect(messages[0]).toEqual(new Uint8Array([1]))
      expect(messages[1]).toEqual(new Uint8Array([2]))
    })

    it('should handle stream end', async () => {
      const channelId = await openTestChannel('w1')
      const endFn = vi.fn()
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onEnd(endFn)

      sendStreamEndFromWorker(channelId, handle.requestId)

      expect(endFn).toHaveBeenCalledOnce()
    })

    it('should handle stream error', async () => {
      const channelId = await openTestChannel('w1')
      const errorFn = vi.fn()
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onError(errorFn)

      sendStreamErrorFromWorker(channelId, handle.requestId, 'stream broke')

      expect(errorFn).toHaveBeenCalledOnce()
      expect(errorFn.mock.calls[0][0].message).toBe('stream broke')
    })

    it('should throw if channel is not open', async () => {
      expect(() => mgr.stream('nonexistent', 'WatchEvents', new Uint8Array())).toThrow('channel not open')
    })

    it('surfaces a send failure (throws + unregisters) when the socket is not OPEN', async () => {
      const channelId = await openTestChannel('w1')
      // Grab the channel object up front: the throw's onSendFailure closes the
      // channel (a 'transport' error) and removes it from the manager's map, but
      // stream()'s catch unregisters the listener on THIS same object first.
      const internals = channelInternals(mgr, channelId)
      // The socket is CLOSED but the channel is still 'verified' (a stale socket
      // whose close event was dropped). stream()'s initial request send must
      // throw rather than be silently dropped, or the stream listener stays
      // registered producing no data and no error until the channel is torn down.
      mockWs.readyState = MockWebSocket.CLOSED
      expect(() => mgr.stream(channelId, 'WatchEvents', new Uint8Array())).toThrow(/WebSocket not open/)
      // The catch unregistered the listener rather than leaking it.
      expect(internals.streamListeners.size).toBe(0)
    })

    it('should unregister the stream even when onError throws', async () => {
      const channelId = await openTestChannel('w1')
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onError(() => {
        throw new Error('listener bug')
      })

      // A throwing terminal callback must not skip unregisterRequest: before the
      // fix the throw propagated out of deliverStream, leaving the listener and
      // its reassembly slot registered forever (four of them exhaust
      // MAX_INCOMPLETE_CHUNKED and wedge the channel). It must also not unwind
      // into the WebSocket message dispatch.
      expect(() => sendStreamErrorFromWorker(channelId, handle.requestId, 'stream broke')).not.toThrow()
      expect(channelInternals(mgr, channelId).streamListeners.has(handle.requestId)).toBe(false)
    })

    it('should unregister the stream even when onEnd throws', async () => {
      const channelId = await openTestChannel('w1')
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onEnd(() => {
        throw new Error('listener bug')
      })

      expect(() => sendStreamEndFromWorker(channelId, handle.requestId)).not.toThrow()
      expect(channelInternals(mgr, channelId).streamListeners.has(handle.requestId)).toBe(false)
    })

    it('should keep the stream live and isolated when onMessage throws mid-stream', async () => {
      const channelId = await openTestChannel('w1')
      const received: Uint8Array[] = []
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      let calls = 0
      handle.onMessage((msg) => {
        calls++
        if (calls === 1)
          throw new Error('listener bug')
        received.push(msg.payload)
      })

      // A throwing per-chunk callback must be isolated (safeCall) so it neither
      // unwinds into the WS message dispatch nor stops later chunks arriving.
      expect(() => sendStreamMessageFromWorker(channelId, handle.requestId, new Uint8Array([1]))).not.toThrow()
      expect(() => sendStreamMessageFromWorker(channelId, handle.requestId, new Uint8Array([2]))).not.toThrow()
      expect(received).toEqual([new Uint8Array([2])])
      // Still live: a non-terminal throw does not unregister.
      expect(channelInternals(mgr, channelId).streamListeners.has(handle.requestId)).toBe(true)
    })
  })

  describe('getOrOpenChannel', () => {
    it('should return existing channel for same worker', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2 = await mgr.getOrOpenChannel('w1')
      expect(ch2).toBe(ch1)
    })

    it('should open new channel for different worker', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2Promise = mgr.getOrOpenChannel('w2')
      await flushMicrotasks()
      simulatePingAccept()
      const ch2 = await ch2Promise
      expect(ch2).not.toBe(ch1)
    })

    it('should open new channel if existing one is closed', async () => {
      const ch1 = await openTestChannel('w1')
      await mgr.closeChannel(ch1)
      const ch2Promise = mgr.getOrOpenChannel('w1')
      await flushMicrotasks()
      simulatePingAccept()
      const ch2 = await ch2Promise
      expect(ch2).not.toBe(ch1)
      expect(mgr.isOpen(ch2)).toBe(true)
    })

    // The three staleReason branches: a pooled channel is rotated out rather than
    // reused when it aged out, needs a rekey, or was opened under a now-stale
    // identity. Before staleReason was extracted these paths had no test.
    it('reopens a pooled channel that has aged past its max age', async () => {
      const ch1 = await openTestChannel('w1')
      // Backdate the open so the age check trips.
      ;(mgr as any).channels.get(ch1).openedAt = 0
      const ch2Promise = mgr.getOrOpenChannel('w1')
      await flushMicrotasks()
      simulatePingAccept()
      const ch2 = await ch2Promise
      expect(ch2).not.toBe(ch1)
      expect(mgr.isOpen(ch1)).toBe(false)
    })

    it('reopens a pooled channel whose send session needs a rekey', async () => {
      const ch1 = await openTestChannel('w1')
      ;(mgr as any).channels.get(ch1).session.send.needsRekey = () => true
      const ch2Promise = mgr.getOrOpenChannel('w1')
      await flushMicrotasks()
      simulatePingAccept()
      const ch2 = await ch2Promise
      expect(ch2).not.toBe(ch1)
      expect(mgr.isOpen(ch1)).toBe(false)
    })

    it('reopens a pooled channel whose hub-authenticated identity has drifted', async () => {
      sessions.clear()
      const driftWs = new MockWebSocket()
      let hubUserId = 'user-a'
      let expected: string | undefined = 'user-a'
      const base = createMockTransport(driftWs)
      const transport: ChannelTransport = {
        ...base,
        async openChannel(workerId: string, handshakePayload: Uint8Array) {
          const r = await base.openChannel(workerId, handshakePayload)
          return { ...r, userId: hubUserId }
        },
      }
      const driftMgr = new ChannelManager(transport, {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        expectedUserId: () => expected,
      })
      try {
        // Open the pooled channel as user-a (expectation matches).
        const openPromise = driftMgr.openChannel('w1')
        await flushMicrotasks()
        driftWs.simulateOpen()
        await flushMicrotasks()
        simulatePingAccept(driftWs)
        const ch1 = await openPromise

        // This tab logs out and back in as user-b; the Hub authenticates the reopen
        // as user-b too. The pooled user-a channel must be rotated out, not reused.
        hubUserId = 'user-b'
        expected = 'user-b'
        const ch2Promise = driftMgr.getOrOpenChannel('w1')
        await flushMicrotasks()
        simulatePingAccept(driftWs)
        const ch2 = await ch2Promise
        expect(ch2).not.toBe(ch1)
        expect((driftMgr as any).channels.get(ch2).userId).toBe('user-b')
      }
      finally {
        driftMgr.closeAll()
      }
    })
  })

  describe('close notification', () => {
    it('should handle close=true as close notification', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array())

      // Simulate close notification (close: true).
      mockWs.simulateMessage(encodeCloseMessage(channelId))

      await expect(callPromise).rejects.toThrow('channel closed by server')
      expect(mgr.isOpen(channelId)).toBe(false)
    })

    it('should reject pending requests on close notification', async () => {
      const channelId = await openTestChannel('w1')
      const call1 = mgr.call(channelId, 'M1', new Uint8Array())
      const call2 = mgr.call(channelId, 'M2', new Uint8Array())

      mockWs.simulateMessage(encodeCloseMessage(channelId))

      await expect(call1).rejects.toThrow('channel closed by server')
      await expect(call2).rejects.toThrow('channel closed by server')
    })

    it('should error active streams on close notification', async () => {
      const channelId = await openTestChannel('w1')
      const errorFn = vi.fn()
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onError(errorFn)

      mockWs.simulateMessage(encodeCloseMessage(channelId))

      expect(errorFn).toHaveBeenCalledOnce()
      expect(errorFn.mock.calls[0][0].message).toBe('channel closed by server')
    })
  })

  describe('decrypt failure', () => {
    it('should close the channel on decrypt failure', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array())

      // Send a message with corrupted ciphertext (not encrypted with the correct key/nonce).
      const corruptCiphertext = new Uint8Array(32)
      corruptCiphertext.fill(0xFF)
      mockWs.simulateMessage(encodeWireMessage(channelId, corruptCiphertext, { id: 1 }))

      // The channel should be closed and the pending request rejected.
      await expect(callPromise).rejects.toThrow('channel closed')
      expect(mgr.isOpen(channelId)).toBe(false)
    })

    it('should close only the affected channel on decrypt failure', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2 = await openTestChannel('w2')

      // Corrupt ciphertext on ch1.
      const corruptCiphertext = new Uint8Array(32)
      corruptCiphertext.fill(0xFF)
      mockWs.simulateMessage(encodeWireMessage(ch1, corruptCiphertext, { id: 1 }))

      expect(mgr.isOpen(ch1)).toBe(false)
      expect(mgr.isOpen(ch2)).toBe(true)
    })

    it('should error active streams on decrypt failure', async () => {
      const channelId = await openTestChannel('w1')
      const endFn = vi.fn()
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onEnd(endFn)

      const corruptCiphertext = new Uint8Array(32)
      corruptCiphertext.fill(0xFF)
      mockWs.simulateMessage(encodeWireMessage(channelId, corruptCiphertext, { id: 1 }))

      expect(endFn).toHaveBeenCalledOnce()
      expect(mgr.isOpen(channelId)).toBe(false)
    })
  })

  describe('webSocket close', () => {
    it('should reject all pending requests when WebSocket closes', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array())

      mockWs.simulateClose()

      await expect(callPromise).rejects.toThrow('channel disconnected')
      expect(mgr.isOpen(channelId)).toBe(false)
    })

    it('should error all streams when WebSocket closes', async () => {
      const channelId = await openTestChannel('w1')
      const errorFn = vi.fn()
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onError(errorFn)

      mockWs.simulateClose()

      expect(errorFn).toHaveBeenCalledOnce()
      expect(errorFn.mock.calls[0][0].message).toBe('channel disconnected')
    })

    it('should close all channels when WebSocket closes', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2 = await openTestChannel('w2')

      mockWs.simulateClose()

      expect(mgr.isOpen(ch1)).toBe(false)
      expect(mgr.isOpen(ch2)).toBe(false)
    })

    // A prior socket's close must not clobber a successor dial started while the
    // prior was CLOSING. The `this.ws === ws` guard skips a close whose successor
    // already OPENED, but a successor still DIALING leaves this.ws pointing at the
    // closing socket while this.wsPromise holds the successor's dial. Nulling
    // wsPromise / clearing the open dedup there orphans the successor's socket and
    // lets a third dial start.
    it('preserves a successor dial when the prior socket closes while still dialing', async () => {
      const wsA = new MockWebSocket()
      const wsB = new MockWebSocket()
      const wsC = new MockWebSocket()
      const queue = [wsA, wsB, wsC]
      let dialCount = 0
      const transport: ChannelTransport = {
        ...createMockTransport(wsA),
        createWebSocket() {
          dialCount++
          return queue.shift()!
        },
      }
      const mgr2 = new ChannelManager(transport, { handshake1: mockHandshake1, handshake2: mockHandshake2 })
      let open2: Promise<string> | undefined
      let open3: Promise<string> | undefined
      try {
        // 1. Open a channel on wsA so this.ws === wsA and its close listener is attached.
        const open1 = mgr2.openChannel('w1')
        await flushMicrotasks()
        wsA.simulateOpen()
        await flushMicrotasks()
        simulatePingAccept(wsA)
        await open1
        expect(dialCount).toBe(1)

        // 2. wsA leaves OPEN but its close event has not fired; a concurrent open dials
        //    wsB as the successor (this.wsPromise = promise_B, this.ws still wsA).
        wsA.readyState = MockWebSocket.CLOSING
        open2 = mgr2.openChannel('w2')
        open2.catch(() => {})
        await flushMicrotasks()
        expect(dialCount).toBe(2)
        expect(wsB.readyState).toBe(MockWebSocket.CONNECTING)

        // 3. wsA's queued close finally fires. It must NOT clobber wsB's in-flight dial.
        wsA.simulateClose()

        // 4. A further open must dedup onto wsB's dial, never dial a third socket.
        open3 = mgr2.openChannel('w3')
        open3.catch(() => {})
        await flushMicrotasks()
        expect(dialCount).toBe(2)
      }
      finally {
        void open2
        void open3
        mgr2.closeAll()
      }
    })

    // The stale-socket fence covers MESSAGES, not just close: a superseded
    // socket can still deliver frames it buffered before it was replaced, and
    // routing them into the shared channel map would let a stale CLOSE-flag
    // frame drain a live channel (or a stale data frame advance a channel's
    // Noise receive nonce) on the successor's watch.
    it('ignores messages from a superseded socket', async () => {
      const wsA = new MockWebSocket()
      const wsB = new MockWebSocket()
      const queue = [wsA, wsB]
      const transport: ChannelTransport = {
        ...createMockTransport(wsA),
        createWebSocket() {
          return queue.shift()!
        },
      }
      const mgr2 = new ChannelManager(transport, { handshake1: mockHandshake1, handshake2: mockHandshake2 })
      try {
        // 1. Open ch1 on wsA.
        const open1 = mgr2.openChannel('w1')
        await flushMicrotasks()
        wsA.simulateOpen()
        await flushMicrotasks()
        simulatePingAccept(wsA)
        const ch1 = await open1

        // 2. wsA flips CLOSING (its close event still queued); a successor open
        //    dials and installs wsB as the current socket.
        wsA.readyState = MockWebSocket.CLOSING
        const open2 = mgr2.openChannel('w2')
        await flushMicrotasks()
        wsB.simulateOpen()
        await flushMicrotasks()
        simulatePingAccept(wsB)
        await open2

        // 3. wsA delivers a buffered CLOSE-flag frame for ch1. It must be
        //    ignored: ch1 lives in the shared channel map the successor now
        //    serves, and only the CURRENT socket may mutate it.
        wsA.simulateMessage(encodeCloseMessage(ch1))
        expect(mgr2.isOpen(ch1)).toBe(true)
      }
      finally {
        mgr2.closeAll()
      }
    })
  })

  describe('closeAll', () => {
    it('should close all channels and the WebSocket', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2 = await openTestChannel('w2')

      mgr.closeAll()

      expect(mgr.isOpen(ch1)).toBe(false)
      expect(mgr.isOpen(ch2)).toBe(false)
      expect(mockWs.readyState).toBe(MockWebSocket.CLOSED)
    })

    // closeAll snapshots this.channels, so an open still parked on an await when
    // the snapshot was taken would register AFTER it and slip past the eager
    // identity release -- the TOCTOU the closeGeneration guard closes. The lazy
    // staleReason re-check prevents cross-user REUSE either way; this pins that
    // the leaked channel itself must not survive.
    it('does not leave a channel registered when closeAll races an open past its snapshot', async () => {
      const openPromise = mgr.openChannel('w1')
      await flushMicrotasks() // parked at ensureWebSocket, before channels.set
      mgr.closeAll() // eager identity release; snapshot misses the unregistered channel
      mockWs.simulateOpen()
      await flushMicrotasks()
      if (mockWs.sent.length > 0) // without the guard the open sent + must answer its verify ping
        simulatePingAccept()
      await expect(openPromise).rejects.toThrow(ChannelError) // without the guard: resolves
      expect(mgr.hasOpenChannel('w1')).toBe(false)
    })
  })

  describe('webSocket connection failure', () => {
    it('should reject openChannel if WebSocket fails to connect', async () => {
      const openPromise = mgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateError()
      await expect(openPromise).rejects.toThrow('WebSocket connection failed')
    })

    it('should reject openChannel if WebSocket times out', async () => {
      vi.useFakeTimers()
      try {
        const openPromise = mgr.openChannel('w1')
        // Flush microtasks so openChannel reaches ensureWebSocket.
        vi.advanceTimersByTime(0)
        await flushMicrotasks()
        // Set up rejection expectation BEFORE advancing timers
        // to avoid unhandled rejection warning.
        const expectation = expect(openPromise).rejects.toThrow('WebSocket open timed out after 10s')
        // Now advance past the 10s WS open timeout.
        vi.advanceTimersByTime(10_000)
        await flushMicrotasks()
        await expectation
      }
      finally {
        vi.useRealTimers()
      }
    })
  })

  describe('message routing', () => {
    it('should ignore messages for unknown channels', async () => {
      await openTestChannel('w1')
      // Should not throw.
      mockWs.simulateMessage(encodeWireMessage('unknown-channel', new Uint8Array([1, 2, 3])))
    })

    it('should route messages to the correct channel', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2 = await openTestChannel('w2')

      const call1 = mgr.call(ch1, 'M1', new Uint8Array())
      const call2 = mgr.call(ch2, 'M2', new Uint8Array())

      sendResponseFromWorker(ch2, FIRST_TEST_REQUEST_ID, new Uint8Array([20]))
      sendResponseFromWorker(ch1, FIRST_TEST_REQUEST_ID, new Uint8Array([10]))

      const [resp1, resp2] = await Promise.all([call1, call2])
      expect(resp1.payload).toEqual(new Uint8Array([10]))
      expect(resp2.payload).toEqual(new Uint8Array([20]))
    })
  })

  describe('chunking', () => {
    /** Encode a wire message with specific flags. */
    function encodeWireMessageWithFlags(channelId: string, ciphertext: Uint8Array, opts: { correlationId: number, flags: number }): ArrayBuffer {
      const msg = create(ChannelMessageSchema, {
        protocolVersion: 1,
        channelId,
        ciphertext,
        correlationId: BigInt(opts.correlationId),
        flags: opts.flags,
      })
      const data = toBinary(ChannelMessageSchema, msg)
      const buf = new Uint8Array(4 + data.length)
      new DataView(buf.buffer).setUint32(0, data.length)
      buf.set(data, 4)
      return buf.buffer
    }

    it('should send a single chunk for small plaintext', async () => {
      const channelId = await openTestChannel('w1')
      const sentBefore = mockWs.sent.length

      const callPromise = mgr.call(channelId, 'Test', new Uint8Array([1, 2, 3]))

      const sentAfter = mockWs.sent.length
      expect(sentAfter - sentBefore).toBe(1) // Just 1 frame

      const msg = decodeWireMessage(mockWs.sent[sentAfter - 1])
      expect(msg.flags).toBe(0) // UNSPECIFIED

      // Complete the call so it doesn't stay pending during cleanup.
      sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([42]))
      await callPromise
    })

    it('should send multiple chunks for large plaintext', async () => {
      // Create a manager with a small max chunk awareness.
      // We can't easily test actual chunking without matching MAX_CHUNK_SIZE,
      // but we can verify the chunk splitting logic by checking the wire output.
      const channelId = await openTestChannel('w1')
      const sentBefore = mockWs.sent.length

      // Send a request — the payload itself is small enough for one chunk.
      // This just validates the normal path works.
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array(10))
      const sentAfter = mockWs.sent.length
      expect(sentAfter - sentBefore).toBe(1)

      // Complete the call.
      sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([42]))
      const resp = await callPromise
      expect(resp.payload).toEqual(new Uint8Array([42]))
    })

    it('should reassemble multi-chunk response', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array())

      const pair = sessions.get(channelId)!

      // Build a response InnerMessage.
      const resp = create(InnerRpcResponseSchema, {
        payload: new Uint8Array([10, 20, 30, 40, 50]),
        isError: false,
      })
      const envelope = create(InnerMessageSchema, {
        kind: { case: 'response', value: resp },
      })
      const plaintext = toBinary(InnerMessageSchema, envelope)

      // Split the plaintext into 2 chunks (simulate chunking).
      const mid = Math.floor(plaintext.length / 2)
      const chunk1 = plaintext.slice(0, mid)
      const chunk2 = plaintext.slice(mid)

      // Encrypt each chunk separately.
      const ct1 = pair.responder.send.encrypt(chunk1)
      const ct2 = pair.responder.send.encrypt(chunk2)

      // Send chunk1 with flags=MORE.
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct1, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

      // Send chunk2 with flags=UNSPECIFIED (final).
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct2, { correlationId: FIRST_TEST_REQUEST_ID, flags: 0 }))

      const result = await callPromise
      expect(result.payload).toEqual(new Uint8Array([10, 20, 30, 40, 50]))
    })

    // An out-of-spec flags value (e.g. MORE|CLOSE combined, which no conformant
    // sender emits) must be dropped -- not read as "final chunk" and delivered
    // truncated -- and the drop must come AFTER the decrypt so the receive
    // nonce stays in step with the peer (mirrors the Go receivers'
    // channelwire.ChunkContinuation).
    it('drops a frame with out-of-spec flags without desyncing the receive nonce', async () => {
      const channelId = await openTestChannel('w1')
      const pair = sessions.get(channelId)!
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array([1]))

      const resp = create(InnerRpcResponseSchema, { payload: new Uint8Array([9]), isError: false })
      const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
      const ct1 = pair.responder.send.encrypt(toBinary(InnerMessageSchema, envelope))
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct1, { correlationId: FIRST_TEST_REQUEST_ID, flags: 3 }))

      // The dropped frame advanced the receive nonce: the peer's NEXT
      // ciphertext still decrypts and resolves the call.
      sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([42]))
      const result = await callPromise
      expect(result.payload).toEqual(new Uint8Array([42]))
    })

    it('should drop oversized chunked messages', async () => {
      // Create a manager with a very small max message size.
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })

      const openPromise = smallMgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()
      simulatePingAccept()

      const channelId = await openPromise
      const pair = sessions.get(channelId)!

      const callPromise = smallMgr.call(channelId, 'Test', new Uint8Array())

      // Send a chunk that's within limits.
      const chunk1Data = new Uint8Array(30)
      const ct1 = pair.responder.send.encrypt(chunk1Data)
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct1, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

      // Send another chunk that exceeds the 50-byte limit (total 60 > 50).
      const chunk2Data = new Uint8Array(30)
      const ct2 = pair.responder.send.encrypt(chunk2Data)
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct2, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

      // The call should be rejected with an error about the size limit.
      await expect(callPromise).rejects.toThrow('exceeds')

      smallMgr.closeAll()
    })

    // The size limit must hold when the FINAL chunk is what breaches it, not just
    // when a MORE chunk does. A peer chooses its own framing, so a limit enforced on
    // only one of the two paths is not a limit on the message at all -- it just
    // moves the bypass to the other framing.
    it('should drop a chunked message whose final chunk exceeds the limit', async () => {
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })

      const openPromise = smallMgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()
      simulatePingAccept()

      const channelId = await openPromise
      const pair = sessions.get(channelId)!

      const callPromise = smallMgr.call(channelId, 'Test', new Uint8Array())

      // A MORE chunk within limits.
      const ct1 = pair.responder.send.encrypt(new Uint8Array(30))
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct1, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

      // The FINAL chunk (flags: 0) pushes the total to 60 > 50.
      const ct2 = pair.responder.send.encrypt(new Uint8Array(30))
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct2, { correlationId: FIRST_TEST_REQUEST_ID, flags: 0 }))

      await expect(callPromise).rejects.toThrow('exceeds')

      smallMgr.closeAll()
    })

    it('should reject too many incomplete chunked sequences', async () => {
      const channelId = await openTestChannel('w1')
      const pair = sessions.get(channelId)!

      // Start MAX_INCOMPLETE_CHUNKED (4) chunked sequences.
      for (let i = 1; i <= 4; i++) {
        const chunk = new Uint8Array([i])
        const ct = pair.responder.send.encrypt(chunk)
        mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct, { correlationId: i, flags: 1 }))
      }

      // 5th should be dropped (exceeded MAX_INCOMPLETE_CHUNKED).
      const chunk5 = new Uint8Array([5])
      const ct5 = pair.responder.send.encrypt(chunk5)
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct5, { correlationId: 5, flags: 1 }))

      // Channel should still be functional — close notification works.
      mockWs.simulateMessage(encodeCloseMessage(channelId))
      expect(mgr.isOpen(channelId)).toBe(false)
    })

    it('should throw on send when plaintext exceeds maxMessageSize', async () => {
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })

      const openPromise = smallMgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()
      simulatePingAccept()

      const channelId = await openPromise

      // A large payload should cause call() to reject with "message too large".
      await expect(smallMgr.call(channelId, 'Test', new Uint8Array(200))).rejects.toThrow('message too large')

      smallMgr.closeAll()
    })

    it('should reject on final chunk oversize', async () => {
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })

      const openPromise = smallMgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()
      simulatePingAccept()

      const channelId = await openPromise
      const pair = sessions.get(channelId)!

      const callPromise = smallMgr.call(channelId, 'Test', new Uint8Array())

      // Send a first chunk within limits (30 bytes).
      const chunk1 = pair.responder.send.encrypt(new Uint8Array(30))
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk1, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

      // Send a final chunk that pushes over (30 + 30 = 60 > 50).
      const chunk2 = pair.responder.send.encrypt(new Uint8Array(30))
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk2, { correlationId: FIRST_TEST_REQUEST_ID, flags: 0 }))

      await expect(callPromise).rejects.toThrow('exceeds')

      smallMgr.closeAll()
    })

    it('should route chunking errors to stream listeners', async () => {
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })

      const openPromise = smallMgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()
      simulatePingAccept()

      const channelId = await openPromise
      const pair = sessions.get(channelId)!

      const errorFn = vi.fn()
      const handle = smallMgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onError(errorFn)

      // Send chunks that exceed the limit, targeted at the stream's requestId.
      const chunk1 = pair.responder.send.encrypt(new Uint8Array(30))
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk1, { correlationId: handle.requestId, flags: 1 }))

      const chunk2 = pair.responder.send.encrypt(new Uint8Array(30))
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk2, { correlationId: handle.requestId, flags: 1 }))

      expect(errorFn).toHaveBeenCalledOnce()
      expect(errorFn.mock.calls[0][0].message).toContain('exceeds')

      smallMgr.closeAll()
    })

    it('should clear reassembly on close', async () => {
      const channelId = await openTestChannel('w1')
      const pair = sessions.get(channelId)!

      // Start a chunked sequence.
      const chunk = new Uint8Array([1, 2, 3])
      const ct = pair.responder.send.encrypt(chunk)
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

      // Close the channel — should not crash and should clean up.
      await mgr.closeChannel(channelId)
      expect(mgr.isOpen(channelId)).toBe(false)
    })

    /** Open a channel on a manager sharing the suite's mockWs (already connected after the first open). */
    async function openOn(cm: ChannelManager, workerId = 'w1'): Promise<string> {
      const openPromise = cm.openChannel(workerId)
      await flushMicrotasks()
      if (mockWs.readyState !== MockWebSocket.OPEN) {
        mockWs.simulateOpen()
        await flushMicrotasks()
      }
      simulatePingAccept()
      return openPromise
    }

    // A payload the client refuses to send must not leave its bookkeeping behind.
    // call() installs the pending entry and the timeout timer BEFORE the send, inside
    // the Promise executor -- and a throw from an executor rejects the promise without
    // unwinding anything it had set up.
    it('leaves no pending entry behind when a payload is too large to send', async () => {
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })
      try {
        const channelId = await openOn(smallMgr)

        await expect(smallMgr.call(channelId, 'Test', new Uint8Array(200))).rejects.toThrow('message too large')

        expect(channelInternals(smallMgr, channelId).pendingRequests.size).toBe(0)
      }
      finally {
        smallMgr.closeAll()
      }
    })

    // stream() is the worse half: the throw escapes BEFORE the handle is returned, so
    // the caller never learns the requestId and can never removeStreamListener. The
    // entry would live as long as the channel.
    it('leaves no stream listener behind when a payload is too large to send', async () => {
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })
      try {
        const channelId = await openOn(smallMgr)

        expect(() => smallMgr.stream(channelId, 'WatchEvents', new Uint8Array(200))).toThrow('message too large')

        expect(channelInternals(smallMgr, channelId).streamListeners.size).toBe(0)
      }
      finally {
        smallMgr.closeAll()
      }
    })

    // One caller's oversized payload must not cost every other caller the channel: the
    // session never encrypted a byte, so it is still perfectly good.
    it('keeps the channel usable after refusing an oversized payload', async () => {
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })
      try {
        const channelId = await openOn(smallMgr)
        await expect(smallMgr.call(channelId, 'Test', new Uint8Array(200))).rejects.toThrow('message too large')

        expect(smallMgr.isOpen(channelId)).toBe(true)
        // The next call goes out on the same channel and round-trips normally.
        const callPromise = smallMgr.call(channelId, 'Test', new Uint8Array([7]))
        const sentMsg = decodeWireMessage(mockWs.sent.at(-1)!)
        const pair = sessions.get(channelId)!
        pair.responder.receive.decrypt(sentMsg.ciphertext)
        sendResponseFromWorker(channelId, Number(sentMsg.correlationId), new Uint8Array([42]))
        await expect(callPromise).resolves.toMatchObject({ payload: new Uint8Array([42]) })
        expect(await smallMgr.getOrOpenChannel('w1')).toBe(channelId)
      }
      finally {
        smallMgr.closeAll()
      }
    })

    // An encrypt failure is the opposite case: the Noise send state is finished (nonce
    // ceiling) or a chunked send left the peer's receive nonce ahead of ours, so every
    // later send on this channel is garbage. Left in the pool, getOrOpenChannel would
    // hand it to every later caller and each would fail identically for up to an hour.
    it('closes the channel when encrypting a call fails, so pooled callers re-resolve', async () => {
      const channelId = await openTestChannel('w1')
      channelInternals(mgr, channelId).session.send.encrypt = () => {
        throw new Error('noise: nonce overflow (hard limit)')
      }

      await expect(mgr.call(channelId, 'Test', new Uint8Array([1]))).rejects.toThrow('nonce overflow')
      expect(mgr.isOpen(channelId)).toBe(false)
      expect(mgr.hasOpenChannel('w1')).toBe(false)

      // The pooled caller gets a NEW channel rather than the dead one.
      const nextPromise = mgr.getOrOpenChannel('w1')
      await flushMicrotasks()
      simulatePingAccept()
      const nextId = await nextPromise
      expect(nextId).not.toBe(channelId)
      expect(mgr.isOpen(nextId)).toBe(true)
    })

    it('closes the channel when encrypting a stream request fails', async () => {
      const channelId = await openTestChannel('w1')
      channelInternals(mgr, channelId).session.send.encrypt = () => {
        throw new Error('noise: nonce overflow (hard limit)')
      }

      expect(() => mgr.stream(channelId, 'WatchEvents', new Uint8Array([1]))).toThrow('nonce overflow')
      expect(mgr.isOpen(channelId)).toBe(false)
    })
  })

  // ---- Reassembly buffer lifetime ----
  //
  // The peer here is a Hub-relayed worker, so every rule below has to hold against a
  // buggy or hostile one -- these are limits on what an inbound chunk stream can make
  // this client allocate and keep. The Go client of this protocol enforces the same
  // three (backend/tunnel/channel.go).
  describe('reassembly lifetime', () => {
    function encodeChunk(channelId: string, ciphertext: Uint8Array, correlationId: number, more: boolean): ArrayBuffer {
      const msg = create(ChannelMessageSchema, {
        protocolVersion: 1,
        channelId,
        ciphertext,
        correlationId: BigInt(correlationId),
        flags: more ? 1 : 0,
      })
      const data = toBinary(ChannelMessageSchema, msg)
      const buf = new Uint8Array(4 + data.length)
      new DataView(buf.buffer).setUint32(0, data.length)
      buf.set(data, 4)
      return buf.buffer
    }

    async function openSmallMgr(maxMessageSize: number, rpcTimeoutMs?: number): Promise<{ cm: ChannelManager, channelId: string }> {
      sessions.clear()
      const cm = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize,
        ...(rpcTimeoutMs === undefined ? {} : { rpcTimeoutFn: () => rpcTimeoutMs }),
      })
      const openPromise = cm.openChannel('w1')
      await flushMicrotasks()
      if (mockWs.readyState !== MockWebSocket.OPEN) {
        mockWs.simulateOpen()
        await flushMicrotasks()
      }
      simulatePingAccept()
      return { cm, channelId: await openPromise }
    }

    // (a) A breach must POISON the id, not delete its buffer. Deleting it erased the
    // only record that the id had failed: the next chunk found no buffer, passed the
    // cap check (the deleted bytes no longer counted), allocated a fresh one and let
    // the peer re-accumulate to the limit -- silently, since the breach had already
    // unregistered the handler. That cycle repeats for as long as the peer keeps
    // sending.
    it('drops the rest of a chunked message after it breaches the size limit', async () => {
      const { cm, channelId } = await openSmallMgr(50)
      try {
        const pair = sessions.get(channelId)!
        const errorFn = vi.fn()
        const handle = cm.stream(channelId, 'WatchEvents', new Uint8Array())
        handle.onError(errorFn)

        // 30 + 30 > 50: the breach.
        mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
        mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
        expect(errorFn).toHaveBeenCalledOnce()

        // The peer keeps shovelling. Not one byte may be buffered, and the failure is
        // not re-reported.
        for (let i = 0; i < 200; i++) {
          mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
        }
        expect(errorFn).toHaveBeenCalledOnce()

        const ch = channelInternals(cm, channelId)
        const tombstone = ch.reassembly.get(handle.requestId)
        expect(tombstone).toBeDefined()
        expect(tombstone!.poisoned).toBe(true)
        expect(tombstone!.parts).toHaveLength(0)
        expect(tombstone!.total).toBe(0)
        // A tombstone holds no bytes, so it must not hold a cap slot either.
        expect(ch.reassembly.liveCount()).toBe(0)

        // The message's final chunk reaps the tombstone.
        mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, false))
        expect(ch.reassembly.size()).toBe(0)
        expect(errorFn).toHaveBeenCalledOnce()
      }
      finally {
        cm.closeAll()
      }
    })

    // A throwing stream onError must NOT skip the reassembly poison that follows it.
    // failReassembly reports the breach (invoking the app's onError) and THEN
    // tombstones the id. Before safeCall wrapped that onError call, a throw unwound
    // out of failReassembly -> reassemble -> handleMessage, so poison never ran: the
    // id was left un-tombstoned (its buffer already reaped), and every later chunk of
    // the oversize message re-entered the unknown-id warn path -- the per-chunk storm
    // the tombstone exists to prevent.
    it('still poisons a breached id when the stream onError throws', async () => {
      const { cm, channelId } = await openSmallMgr(50)
      try {
        const pair = sessions.get(channelId)!
        let errorCalls = 0
        const handle = cm.stream(channelId, 'WatchEvents', new Uint8Array())
        handle.onError(() => {
          errorCalls++
          throw new Error('listener boom')
        })

        // 30 + 30 > 50: the breach. The throwing onError must not escape the message
        // dispatch.
        mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
        expect(() => {
          mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
        }).not.toThrow()
        expect(errorCalls).toBe(1)

        // The id is tombstoned despite the throw, so the remaining chunks are dropped
        // silently rather than re-accumulating and re-reporting.
        const ch = channelInternals(cm, channelId)
        expect(ch.reassembly.get(handle.requestId)?.poisoned).toBe(true)
        expect(ch.reassembly.liveCount()).toBe(0)

        for (let i = 0; i < 50; i++)
          mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
        expect(errorCalls).toBe(1)
      }
      finally {
        cm.closeAll()
      }
    })

    // (b) Every inbound chunked message answers a request THIS side registered, so a
    // first chunk for an id with no live handler can never complete. Buffering it would
    // pin up to maxMessageSize forever, and four such orphans would exhaust the cap and
    // permanently reject every later chunked message on a healthy channel.
    it('drops a chunk for an unknown correlation id without consuming a cap slot', async () => {
      const channelId = await openTestChannel('w1')
      const pair = sessions.get(channelId)!

      for (let id = 100; id < 104; id++) {
        mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), id, true))
      }

      const ch = channelInternals(mgr, channelId)
      expect(ch.reassembly.size()).toBe(0)
      expect(ch.reassembly.liveCount()).toBe(0)

      // The cap is untouched: a real chunked response still reassembles.
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array())
      const resp = create(InnerRpcResponseSchema, { payload: new Uint8Array([9, 9]), isError: false })
      const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
      const plaintext = toBinary(InnerMessageSchema, envelope)
      const mid = Math.floor(plaintext.length / 2)
      mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(plaintext.slice(0, mid)), FIRST_TEST_REQUEST_ID, true))
      mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(plaintext.slice(mid)), FIRST_TEST_REQUEST_ID, false))

      await expect(callPromise).resolves.toMatchObject({ payload: new Uint8Array([9, 9]) })
    })

    // (c) A reassembly buffer exists only to feed one request, so it must die with it.
    // The timeout drops the handler; nothing else would ever come back for the bytes.
    it('reaps the reassembly buffer when its request times out', async () => {
      const { cm, channelId } = await openSmallMgr(1024, 20)
      try {
        const pair = sessions.get(channelId)!
        const callPromise = cm.call(channelId, 'Test', new Uint8Array())

        mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), FIRST_TEST_REQUEST_ID, true))
        const ch = channelInternals(cm, channelId)
        expect(ch.reassembly.size()).toBe(1)
        expect(ch.reassembly.liveCount()).toBe(1)

        await expect(callPromise).rejects.toThrow('timed out')

        expect(ch.reassembly.size()).toBe(0)
        expect(ch.reassembly.liveCount()).toBe(0)
      }
      finally {
        cm.closeAll()
      }
    })

    it('reaps the reassembly buffer when a stream listener is removed', async () => {
      const channelId = await openTestChannel('w1')
      const pair = sessions.get(channelId)!
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())

      mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
      const ch = channelInternals(mgr, channelId)
      expect(ch.reassembly.size()).toBe(1)

      mgr.removeStreamListener(channelId, handle.requestId)
      expect(ch.reassembly.size()).toBe(0)
      expect(ch.reassembly.liveCount()).toBe(0)
    })
  })

  describe('encryption modes', () => {
    it('should open a channel with classic encryption (X25519 only)', async () => {
      // Create a transport that returns CLASSIC mode.
      const classicWs = new MockWebSocket()
      const classicSessions = new Map<string, { initiator: Session, responder: Session }>()

      const classicTransport: ChannelTransport = {
        async getWorkerHandshakeParams(_workerId: string): Promise<{ keys: WorkerKeyBundle, encryptionMode: EncryptionMode }> {
          return {
            keys: {
              x25519PublicKey: new Uint8Array(32),
              mlkemPublicKey: new Uint8Array(0),
              slhdsaPublicKey: new Uint8Array(0),
            },
            encryptionMode: EncryptionMode.CLASSIC,
          }
        },
        async openChannel(_workerId: string, _handshakePayload: Uint8Array) {
          const channelId = `ch-classic-${Math.random().toString(36).slice(2, 8)}`
          const pair = createTestSession()
          classicSessions.set(channelId, pair)
          return { channelId, handshakePayload: new Uint8Array(48), userId: 'hub-authenticated-user' }
        },
        async closeChannel(_channelId: string) {},
        createWebSocket(): ChannelSocket {
          return classicWs
        },
        async confirmKeyPin(): Promise<KeyPinDecision> {
          return 'accept'
        },
      }

      // Mock classic handshake functions.
      function mockClassicHS1(_remoteX25519Pub: Uint8Array) {
        return {
          handshakeState: {} as any,
          message1: new Uint8Array(48),
        }
      }

      function mockClassicHS2(_state: any, _message2: Uint8Array) {
        const entries = [...classicSessions.entries()]
        const lastEntry = entries.at(-1)
        if (!lastEntry)
          throw new Error('No session registered')
        return lastEntry[1].initiator
      }

      const classicMgr = new ChannelManager(classicTransport, {
        classicHandshake1: mockClassicHS1,
        classicHandshake2: mockClassicHS2,
      })

      const openPromise = classicMgr.openChannel('w-classic')
      await flushMicrotasks()
      classicWs.readyState = MockWebSocket.OPEN
      classicWs.simulateOpen()
      await flushMicrotasks()
      simulatePingAccept(classicWs, classicSessions)

      const channelId = await openPromise
      const pair = classicSessions.get(channelId)!
      expect(classicMgr.isOpen(channelId)).toBe(true)

      // Verify a call works through the encrypted channel.
      const callPromise = classicMgr.call(channelId, 'TestMethod', new Uint8Array([1, 2]))

      const resp = create(InnerRpcResponseSchema, {
        payload: new Uint8Array([3, 4]),
        isError: false,
      })
      const respEnv = create(InnerMessageSchema, {
        kind: { case: 'response', value: resp },
      })
      const respPt = toBinary(InnerMessageSchema, respEnv)
      const respCt = pair.responder.send.encrypt(respPt)
      // The open's session-verifying Ping consumed correlation id 1.
      classicWs.simulateMessage(encodeWireMessage(channelId, respCt, { id: 2 }))

      const result = await callPromise
      expect(result.payload).toEqual(new Uint8Array([3, 4]))

      classicMgr.closeAll()
    })
  })

  describe('observability hooks', () => {
    describe('onStateChange', () => {
      it('should fire after openChannel succeeds', async () => {
        const cb = vi.fn()
        mgr.onStateChange(cb)
        await openTestChannel('w1')
        expect(cb).toHaveBeenCalledOnce()
      })

      it('should fire after closeChannel', async () => {
        const channelId = await openTestChannel('w1')
        const cb = vi.fn()
        mgr.onStateChange(cb)
        await mgr.closeChannel(channelId)
        expect(cb).toHaveBeenCalledOnce()
      })

      it('should fire on WebSocket close (all channels torn down)', async () => {
        await openTestChannel('w1')
        await openTestChannel('w2')
        const cb = vi.fn()
        mgr.onStateChange(cb)
        mockWs.simulateClose()
        expect(cb).toHaveBeenCalledOnce()
      })

      it('should fire on CLOSE sentinel', async () => {
        const channelId = await openTestChannel('w1')
        const cb = vi.fn()
        mgr.onStateChange(cb)
        mockWs.simulateMessage(encodeCloseMessage(channelId))
        expect(cb).toHaveBeenCalledOnce()
      })

      it('should not fire after unsubscribe', async () => {
        const cb = vi.fn()
        const unsub = mgr.onStateChange(cb)
        unsub()
        await openTestChannel('w1')
        expect(cb).not.toHaveBeenCalled()
      })
    })

    describe('onChannelError', () => {
      it('should fire on RPC error (non-transport)', async () => {
        const channelId = await openTestChannel('w1')
        const cb = vi.fn()
        mgr.onChannelError(cb)

        const callPromise = mgr.call(channelId, 'Test', new Uint8Array())
        sendErrorResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, 'rpc failed')
        await expect(callPromise).rejects.toThrow('rpc failed')

        expect(cb).toHaveBeenCalledOnce()
        expect(cb.mock.calls[0][0]).toBe('w1')
        expect(cb.mock.calls[0][1].source).toBe('rpc')
      })

      it('should fire on stream error (non-transport)', async () => {
        const channelId = await openTestChannel('w1')
        const cb = vi.fn()
        mgr.onChannelError(cb)

        const errorFn = vi.fn()
        const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
        handle.onError(errorFn)

        sendStreamErrorFromWorker(channelId, handle.requestId, 'stream broke')

        expect(cb).toHaveBeenCalledOnce()
        expect(cb.mock.calls[0][0]).toBe('w1')
        expect(cb.mock.calls[0][1].source).toBe('stream')
      })

      it('should not fire after unsubscribe', async () => {
        const channelId = await openTestChannel('w1')
        const cb = vi.fn()
        const unsub = mgr.onChannelError(cb)
        unsub()

        const callPromise = mgr.call(channelId, 'Test', new Uint8Array())
        sendErrorResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, 'rpc failed')
        await expect(callPromise).rejects.toThrow('rpc failed')

        expect(cb).not.toHaveBeenCalled()
      })
    })

    describe('hasOpenChannel', () => {
      it('should return true when channel is open', async () => {
        await openTestChannel('w1')
        expect(mgr.hasOpenChannel('w1')).toBe(true)
      })

      it('should return false when no channel exists', () => {
        expect(mgr.hasOpenChannel('w1')).toBe(false)
      })

      it('should return false after channel is closed', async () => {
        const channelId = await openTestChannel('w1')
        await mgr.closeChannel(channelId)
        expect(mgr.hasOpenChannel('w1')).toBe(false)
      })
    })
  })
})

// ---------------------------------------------------------------------------
// getOrOpenChannel concurrency / deduplication
//
// These tests exercise *concurrent* opens, which the main suite's real-crypto
// mock handshake cannot model (mockHandshake2 returns the LAST registered
// session, so two simultaneous opens would clobber each other's crypto state).
// They use a self-contained identity-cipher setup so multiple channels can be
// opened at once without real crypto.
// ---------------------------------------------------------------------------

/** Mock WebSocket that auto-opens on the next microtask (simulates TCP + upgrade). */
class AutoOpenMockWebSocket extends EventTarget {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3

  readyState = AutoOpenMockWebSocket.CONNECTING
  binaryType = 'arraybuffer'

  constructor() {
    super()
    queueMicrotask(() => {
      if (this.readyState === AutoOpenMockWebSocket.CONNECTING) {
        this.readyState = AutoOpenMockWebSocket.OPEN
        this.dispatchEvent(new Event('open'))
      }
    })
  }

  /** Fire the 'close' event and set readyState to CLOSED. */
  simulateClose() {
    this.readyState = AutoOpenMockWebSocket.CLOSED
    this.dispatchEvent(new Event('close'))
  }

  close() {
    this.readyState = AutoOpenMockWebSocket.CLOSED
  }

  send(_data: unknown) {
    // no-op in tests
  }
}

/** Identity-cipher Noise session — encrypt/decrypt are pass-through, no real crypto. */
/**
 * Answer the open-time Ping on an identity-cipher mock channel. openChannel
 * round-trips a no-op Ping to prove the E2EE session decrypts in both directions
 * before it resolves, so a mock worker must reply or the open never completes.
 * The mock sessions encrypt as identity, so the wire message is built directly.
 * The Ping is each channel's first request, hence correlation id 1.
 */
function simulatePingAcceptOnWs(ws: AutoOpenMockWebSocket, channelId: string) {
  const resp = create(InnerRpcResponseSchema, { isError: false })
  const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
  const plaintext = toBinary(InnerMessageSchema, envelope)

  const channelMsg = create(ChannelMessageSchema, {
    protocolVersion: 1,
    channelId,
    ciphertext: plaintext,
    correlationId: 1n,
  })
  const msgBytes = toBinary(ChannelMessageSchema, channelMsg)
  const frame = new Uint8Array(4 + msgBytes.length)
  new DataView(frame.buffer).setUint32(0, msgBytes.length)
  frame.set(msgBytes, 4)
  ws.dispatchEvent(new MessageEvent('message', { data: frame.buffer }))
}

/**
 * Fail the open-time Ping on an identity-cipher mock channel: the worker answers, but
 * with an error, i.e. the session is proven NOT to work.
 */
function simulatePingErrorOnWs(ws: AutoOpenMockWebSocket, channelId: string, message: string) {
  const resp = create(InnerRpcResponseSchema, { isError: true, errorMessage: message, errorCode: 2 })
  const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
  const plaintext = toBinary(InnerMessageSchema, envelope)

  const channelMsg = create(ChannelMessageSchema, {
    protocolVersion: 1,
    channelId,
    ciphertext: plaintext,
    correlationId: 1n,
  })
  const msgBytes = toBinary(ChannelMessageSchema, channelMsg)
  const frame = new Uint8Array(4 + msgBytes.length)
  new DataView(frame.buffer).setUint32(0, msgBytes.length)
  frame.set(msgBytes, 4)
  ws.dispatchEvent(new MessageEvent('message', { data: frame.buffer }))
}

function makeMockSession(): Session {
  return {
    send: {
      encrypt: (pt: Uint8Array) => pt,
      decrypt: (ct: Uint8Array) => ct,
      needsRekey: () => false,
    },
    receive: {
      encrypt: (pt: Uint8Array) => pt,
      decrypt: (ct: Uint8Array) => ct,
      needsRekey: () => false,
    },
  } as Session
}

function makeMockTransport(onCreateWs: () => AutoOpenMockWebSocket): ChannelTransport {
  return {
    getWorkerHandshakeParams: vi.fn().mockResolvedValue({
      keys: {
        x25519PublicKey: new Uint8Array(32),
        mlkemPublicKey: new Uint8Array(0),
        slhdsaPublicKey: new Uint8Array(0),
      } satisfies WorkerKeyBundle,
      encryptionMode: EncryptionMode.CLASSIC,
    }),
    openChannel: vi.fn().mockResolvedValue({
      channelId: 'ch-1',
      handshakePayload: new Uint8Array(48),
      // The Hub always names the authenticated user; openChannel rejects without it.
      userId: 'user-1',
    }),
    closeChannel: vi.fn().mockResolvedValue(undefined),
    createWebSocket: () => onCreateWs(),
    confirmKeyPin: vi.fn().mockResolvedValue('accept' as const),
  }
}

describe('channelManager getOrOpenChannel deduplication', () => {
  it('should return the same channel for concurrent calls to the same worker', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    let channelCounter = 0
    const transport = makeMockTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    // Each openChannel call gets a unique channel ID so we can detect duplicates.
    transport.openChannel = vi.fn().mockImplementation(async () => ({
      channelId: `ch-${++channelCounter}`,
      handshakePayload: new Uint8Array(48),
      // The Hub always names the authenticated user; openChannel rejects without it.
      userId: 'user-1',
    }))

    const cm = new ChannelManager(transport, {
      classicHandshake1: (_rs: Uint8Array) => ({
        message1: new Uint8Array(48),
        handshakeState: {} as any,
      }),
      classicHandshake2: (_hs: any, _payload: Uint8Array) => makeMockSession(),
    })

    // Launch two concurrent getOrOpenChannel calls for the same worker.
    const p1 = cm.getOrOpenChannel('worker-1')
    const p2 = cm.getOrOpenChannel('worker-1')

    // Let the handshake + WebSocket open progress.
    await new Promise(r => setTimeout(r, 10))
    // The single deduplicated open still verifies its session with a Ping.
    simulatePingAcceptOnWs(ws!, 'ch-1')

    const [ch1, ch2] = await Promise.all([p1, p2])

    // Both should resolve to the same channel — only one openChannel call.
    expect(ch1).toBe(ch2)
    expect(transport.openChannel).toHaveBeenCalledTimes(1)

    cm.closeAll()
  })

  it('should open separate channels for different workers', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    let channelCounter = 0
    const transport = makeMockTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    transport.openChannel = vi.fn().mockImplementation(async () => ({
      channelId: `ch-${++channelCounter}`,
      handshakePayload: new Uint8Array(48),
      // The Hub always names the authenticated user; openChannel rejects without it.
      userId: 'user-1',
    }))

    const cm = new ChannelManager(transport, {
      classicHandshake1: (_rs: Uint8Array) => ({
        message1: new Uint8Array(48),
        handshakeState: {} as any,
      }),
      classicHandshake2: (_hs: any, _payload: Uint8Array) => makeMockSession(),
    })

    // Launch concurrent getOrOpenChannel calls for different workers.
    const p1 = cm.getOrOpenChannel('worker-1')
    const p2 = cm.getOrOpenChannel('worker-2')

    await new Promise(r => setTimeout(r, 10))
    // Each open verifies its own session with a Ping.
    simulatePingAcceptOnWs(ws!, 'ch-1')
    simulatePingAcceptOnWs(ws!, 'ch-2')

    const [ch1, ch2] = await Promise.all([p1, p2])

    expect(ch1).not.toBe(ch2)
    expect(transport.openChannel).toHaveBeenCalledTimes(2)

    cm.closeAll()
  })
})

// ---------------------------------------------------------------------------
// Open-time verification, key pinning and identity on POOLED channels.
//
// All three only misbehave when opens overlap or outlive an identity, which the main
// suite's real-crypto harness cannot model (mockHandshake2 hands back the LAST
// registered session). These reuse the identity-cipher setup above.
// ---------------------------------------------------------------------------

/** Advance real timers enough for the mock handshake and the WS auto-open to settle. */
function settle(): Promise<void> {
  return new Promise(r => setTimeout(r, 10))
}

/**
 * Wait until `channelId`'s open has reached its verification Ping, which is the point a
 * mock worker can answer it.
 *
 * Polls the manager rather than sleeping a fixed interval because how long an open takes
 * to get here is genuinely variable: a key-mismatch open awaits the user prompt and a
 * dynamic import('./fingerprint'), which under a loaded full suite comfortably outruns a
 * 10ms sleep. Answering a ping that has not been sent yet leaves the open hanging until
 * the test times out. The channel is inserted and the ping sent in one synchronous step,
 * so observing the channel at all means the ping is on the wire.
 */
async function waitForPendingChannel(cm: ChannelManager, channelId: string): Promise<void> {
  const channels = (cm as unknown as { channels: Map<string, unknown> }).channels
  for (let i = 0; i < 400; i++) {
    if (channels.has(channelId))
      return
    await new Promise(r => setTimeout(r, 5))
  }
  throw new Error(`channel ${channelId} never reached its verification ping`)
}

function makeCountingTransport(onCreateWs: () => AutoOpenMockWebSocket, userId: () => string = () => 'user-1'): ChannelTransport {
  const transport = makeMockTransport(onCreateWs)
  let channelCounter = 0
  transport.openChannel = vi.fn().mockImplementation(async () => ({
    channelId: `ch-${++channelCounter}`,
    handshakePayload: new Uint8Array(48),
    userId: userId(),
  }))
  return transport
}

function makeIdentityCipherManager(transport: ChannelTransport, opts?: { expectedUserId?: () => string | undefined }): ChannelManager {
  return new ChannelManager(transport, {
    classicHandshake1: (_rs: Uint8Array) => ({ message1: new Uint8Array(48), handshakeState: {} as any }),
    classicHandshake2: (_hs: any, _payload: Uint8Array) => makeMockSession(),
    ...opts,
  })
}

describe('channelManager open-time verification', () => {
  // The whole point of the open-time Ping is that a session broken in either direction
  // never reaches a caller -- and channels are POOLED, so "a caller" includes everyone
  // who asks for this worker while the ping is still in flight. The channel has to be
  // in the manager's map for the ping's own reply to route, so presence in the map
  // cannot be what makes it available.
  it('does not hand out a channel whose verification ping is still in flight', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    const cm = makeIdentityCipherManager(transport)
    try {
      const open = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')

      // The ping is on the wire: the channel exists but is unproven.
      expect(cm.hasOpenChannel('worker-1')).toBe(false)
      expect(channelInternals(cm, 'ch-1').state).toBe('opening')

      let racerResult: string | null = null
      const racer = cm.getOrOpenChannel('worker-1').then((id) => {
        racerResult = id
        return id
      })
      await settle()

      // The racer must be waiting on the SAME open, not holding the unproven channel.
      expect(racerResult).toBeNull()
      expect(transport.openChannel).toHaveBeenCalledTimes(1)

      simulatePingAcceptOnWs(ws!, 'ch-1')

      expect(await open).toBe('ch-1')
      expect(await racer).toBe('ch-1')
      expect(cm.hasOpenChannel('worker-1')).toBe(true)
      expect(transport.openChannel).toHaveBeenCalledTimes(1)
    }
    finally {
      cm.closeAll()
    }
  })

  // The failure case is the one that mattered: a racer handed the unverified channel
  // was left holding an id the open then deleted locally and rolled back at the Hub.
  it('rejects a racing getOrOpenChannel when the verification ping fails', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    const cm = makeIdentityCipherManager(transport)
    try {
      const open = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')
      const racer = cm.getOrOpenChannel('worker-1')
      await settle()

      simulatePingErrorOnWs(ws!, 'ch-1', 'session is dead')

      await expect(open).rejects.toThrow('session is dead')
      await expect(racer).rejects.toThrow('session is dead')
      expect(cm.hasOpenChannel('worker-1')).toBe(false)
      expect(cm.isOpen('ch-1')).toBe(false)
      // The Hub-side registration was rolled back.
      expect(transport.closeChannel).toHaveBeenCalledWith('ch-1')
    }
    finally {
      cm.closeAll()
    }
  })

  // A failed open deletes the channel from the map, which puts it beyond the reach of
  // closeChannel and of the WebSocket teardown -- so anything registered on it has to
  // be settled right here or it is settled by nothing at all, and waits out its own
  // 15s RPC timeout on a channel that no longer exists.
  it('rejects requests registered on the channel while its verification ping was in flight', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    const cm = makeIdentityCipherManager(transport)
    try {
      const open = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')

      // A caller holding the id (the ping's channel is 'ch-1') issues work on it.
      const raced = cm.call('ch-1', 'Test', new Uint8Array())
      const streamErr = vi.fn()
      cm.stream('ch-1', 'WatchEvents', new Uint8Array()).onError(streamErr)

      simulatePingErrorOnWs(ws!, 'ch-1', 'session is dead')
      await expect(open).rejects.toThrow('session is dead')

      // Settled now -- not in 15 seconds, and not never.
      await expect(raced).rejects.toThrow('session is dead')
      expect(streamErr).toHaveBeenCalledOnce()
    }
    finally {
      cm.closeAll()
    }
  })
})

describe('channelManager key pinning across concurrent opens', () => {
  beforeEach(() => {
    ChannelManager.clearAllKeyPins()
  })

  afterEach(() => {
    ChannelManager.clearAllKeyPins()
  })

  /** A transport whose worker keys are per-worker and mutable, so a key can be rotated mid-test. */
  function makeKeyedTransport(onCreateWs: () => AutoOpenMockWebSocket, keyByWorker: Map<string, number>): ChannelTransport {
    const transport = makeCountingTransport(onCreateWs)
    transport.getWorkerHandshakeParams = vi.fn().mockImplementation(async (workerId: string) => ({
      keys: {
        x25519PublicKey: new Uint8Array(32).fill(keyByWorker.get(workerId)!),
        mlkemPublicKey: new Uint8Array(0),
        slhdsaPublicKey: new Uint8Array(0),
      } satisfies WorkerKeyBundle,
      encryptionMode: EncryptionMode.CLASSIC,
    }))
    return transport
  }

  // KEY_KEY_PINS holds EVERY worker's pin in one value, and opens to different workers
  // are not serialized (the open dedup is keyed by worker). Reading the map before the
  // prompt/handshake/WebSocket awaits and writing it back after made each open an
  // unserialized read-modify-write: the later writer's snapshot predated the earlier
  // one's pin, so it silently erased it.
  it('keeps both pins when opens to different workers interleave', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    const cm = makeIdentityCipherManager(transport)
    try {
      const p1 = cm.getOrOpenChannel('w1')
      const p2 = cm.getOrOpenChannel('w2')
      await waitForPendingChannel(cm, 'ch-1')
      await waitForPendingChannel(cm, 'ch-2')
      simulatePingAcceptOnWs(ws!, 'ch-1')
      simulatePingAcceptOnWs(ws!, 'ch-2')
      await Promise.all([p1, p2])

      expect(Object.keys(localStorageGet<Record<string, unknown>>(KEY_KEY_PINS) ?? {}).sort()).toEqual(['w1', 'w2'])
    }
    finally {
      cm.closeAll()
    }
  })

  // Why the lost pin is a security bug and not a lost preference: with w1's pin gone,
  // the next open reads no pin at all, takes the FIRST-USE branch, and silently pins
  // whatever key the Hub serves. No prompt, no error -- the exact key substitution the
  // TOFU prompt exists to catch.
  it('prompts on a key change for a worker whose pin was written during a concurrent open', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const keys = new Map([['w1', 1], ['w2', 2]])
    const transport = makeKeyedTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    }, keys)
    const cm = makeIdentityCipherManager(transport)
    try {
      const p1 = cm.getOrOpenChannel('w1')
      const p2 = cm.getOrOpenChannel('w2')
      await waitForPendingChannel(cm, 'ch-1')
      await waitForPendingChannel(cm, 'ch-2')
      simulatePingAcceptOnWs(ws!, 'ch-1')
      simulatePingAcceptOnWs(ws!, 'ch-2')
      await Promise.all([p1, p2])
      expect(transport.confirmKeyPin).not.toHaveBeenCalled()

      // w1 now presents a different key.
      keys.set('w1', 9)
      const p3 = cm.openChannel('w1')
      await waitForPendingChannel(cm, 'ch-3')
      simulatePingAcceptOnWs(ws!, 'ch-3')
      await p3

      expect(transport.confirmKeyPin).toHaveBeenCalledOnce()
      expect(transport.confirmKeyPin).toHaveBeenCalledWith('w1', expect.any(String), expect.any(String))
      // The accepted key replaced the old pin, and w2's is still there.
      expect(Object.keys(localStorageGet<Record<string, unknown>>(KEY_KEY_PINS) ?? {}).sort()).toEqual(['w1', 'w2'])
    }
    finally {
      cm.closeAll()
    }
  })

  // The pin is only worth recording once a channel to that key has proven itself: every
  // exit before the ping rolls the open back, so a key that never worked must not be
  // the one a later open silently trusts.
  it('does not pin a key whose open failed verification', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    })
    const cm = makeIdentityCipherManager(transport)
    try {
      const open = cm.getOrOpenChannel('w1')
      await waitForPendingChannel(cm, 'ch-1')
      simulatePingErrorOnWs(ws!, 'ch-1', 'session is dead')
      await expect(open).rejects.toThrow('session is dead')

      expect(localStorageGet<Record<string, unknown>>(KEY_KEY_PINS) ?? {}).toEqual({})
    }
    finally {
      cm.closeAll()
    }
  })
})

describe('channelManager pooled channel identity', () => {
  // A pooled channel carries the identity the Hub authenticated its OPEN as, for up to
  // an hour. On a shared machine a tab logs out and back in as B; without this check
  // every worker RPC B's page issues would keep running on the worker AS A, because the
  // pool only ever looked at age and rekey.
  it('does not reuse a pooled channel after the expected identity changes', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    let identity = 'user-a'
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    }, () => identity)
    const cm = makeIdentityCipherManager(transport, { expectedUserId: () => identity })
    try {
      const first = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')
      simulatePingAcceptOnWs(ws!, 'ch-1')
      expect(await first).toBe('ch-1')

      // Same tab, same manager, different user.
      identity = 'user-b'

      const second = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-2')
      simulatePingAcceptOnWs(ws!, 'ch-2')

      expect(await second).toBe('ch-2')
      expect(cm.isOpen('ch-1')).toBe(false)
      expect(transport.openChannel).toHaveBeenCalledTimes(2)
    }
    finally {
      cm.closeAll()
    }
  })

  // hasOpenChannel drives the worker "connected" indicator, and it must agree with the
  // reuse path on identity drift: a pooled channel authenticated as a DIFFERENT user
  // is one getOrOpenChannel would reject and reopen, so reporting "connected" for it
  // would claim a live link the current user cannot use as themselves. (Age / rekey
  // are deliberately NOT excluded here -- their rotation is transparent.)
  it('hasOpenChannel reports a drifted-identity channel as not open', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    let identity = 'user-a'
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    }, () => identity)
    const cm = makeIdentityCipherManager(transport, { expectedUserId: () => identity })
    try {
      const first = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')
      simulatePingAcceptOnWs(ws!, 'ch-1')
      expect(await first).toBe('ch-1')
      expect(cm.hasOpenChannel('worker-1')).toBe(true)

      // Same tab, same manager, different user: the pooled channel is still open but
      // authenticated as user-a, so it no longer counts as connected for user-b.
      identity = 'user-b'
      expect(cm.hasOpenChannel('worker-1')).toBe(false)
    }
    finally {
      cm.closeAll()
    }
  })

  // The check must not churn the pool when nothing changed.
  it('reuses a pooled channel while the expected identity is unchanged', async () => {
    let ws: AutoOpenMockWebSocket | null = null
    const transport = makeCountingTransport(() => {
      ws = new AutoOpenMockWebSocket()
      return ws
    }, () => 'user-a')
    const cm = makeIdentityCipherManager(transport, { expectedUserId: () => 'user-a' })
    try {
      const first = cm.getOrOpenChannel('worker-1')
      await waitForPendingChannel(cm, 'ch-1')
      simulatePingAcceptOnWs(ws!, 'ch-1')
      expect(await first).toBe('ch-1')

      expect(await cm.getOrOpenChannel('worker-1')).toBe('ch-1')
      expect(transport.openChannel).toHaveBeenCalledTimes(1)
    }
    finally {
      cm.closeAll()
    }
  })
})

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
  ) as { maxPlaintextPerChunk: number, maxMessageSize: number, maxIncompleteChunked: number, pingMethod: string }

  it('match the cross-language fixture the Go side also asserts', () => {
    expect(MAX_CHUNK_SIZE).toBe(limits.maxPlaintextPerChunk)
    expect(DEFAULT_MAX_MESSAGE_SIZE).toBe(limits.maxMessageSize)
    expect(MAX_INCOMPLETE_CHUNKED).toBe(limits.maxIncompleteChunked)
    expect(PING_METHOD).toBe(limits.pingMethod)
  })
})
