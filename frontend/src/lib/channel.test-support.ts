import type { ChannelSocket, ChannelTransport, WorkerKeyBundle } from './channel'
import type { KeyPinConfirmFn } from './keyPinStore'
import type { Session } from './noise'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { chacha20poly1305 } from '@noble/ciphers/chacha.js'
import { beforeEach, expect, vi } from 'vitest'
import { DEFAULT_MAX_MESSAGE_SIZE, SESSION_KEY_HARD_CEILING_MS, SESSION_KEY_MAX_AGE_MS } from '~/generated/contracts/wire'
import {
  ChannelMessageSchema,
  EncryptionMode,
  InnerMessageSchema,
  InnerRpcResponseSchema,
  InnerStreamMessageSchema,
  RekeyAckSchema,
  RekeyRejectSchema,
} from '~/generated/proto/leapmux/v1/channel_pb'
import { ChannelManager } from './channel'
import { frameBytes, unframeBytes } from './channelFraming'
import { clearAllKeyPins, KeyPinStore } from './keyPinStore'
import { generateRekeyEphemeral } from './noise-hybrid'

/** Accepting TOFU store for tests that pin on first use / accept key rotation. */
export function acceptingKeyPins(confirmKeyPin?: KeyPinConfirmFn): KeyPinStore {
  return new KeyPinStore({
    confirmKeyPin: confirmKeyPin ?? (async () => 'accept'),
  })
}

// Isolate TOFU pins across suites: managers that omit keyPins use the fail-closed
// default store, and a leftover pin from another test would reject opens that used
// to auto-accept via transport.confirmKeyPin.
beforeEach(() => {
  clearAllKeyPins()
})

export { clearAllKeyPins }

/** Age a channel past SESSION_KEY_MAX_AGE_MS using the monotonic clock. */
export function agePastMaxAge(ch: { lastRekeyAt: number }): void {
  ch.lastRekeyAt = performance.now() - SESSION_KEY_MAX_AGE_MS - 1
}

/** Age a channel past SESSION_KEY_HARD_CEILING_MS using the monotonic clock. */
export function agePastHardCeiling(ch: { lastRekeyAt: number }): void {
  ch.lastRekeyAt = performance.now() - SESSION_KEY_HARD_CEILING_MS
}

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

  /**
   * Test double for the fresh-DH rekey: ignore the secrets (the test harness
   * uses deterministic tweaked keys so paired sessions stay in sync without
   * real HKDF) and apply the same deterministic key tweak the old rekey() did.
   * Both directions of a paired session call this with mirrored keys, so they
   * produce matching new keys.
   */
  rekeyWithSecret(_dhSecret: Uint8Array, _pqSecret: Uint8Array | null, _retainPrev: boolean): void {
    const next = new Uint8Array(this.k)
    for (let i = 0; i < next.length; i++)
      next[i] = (next[i] + 1) & 0xFF
    this.k = next
    this.n = 0
  }

  /** No-op for the test double (production retains a prev key for the grace window). */
  clearPrev(): void {}

  nonce(): number {
    return this.n
  }
}

/** Create a matched pair of cipher states (send/receive mirrors). */
export function createTestSession(): { initiator: Session, responder: Session } {
  const key1 = new Uint8Array(32)
  const key2 = new Uint8Array(32)
  key1[0] = 1 // Different keys for send/receive
  key2[0] = 2
  return {
    initiator: { send: new TestCipherState(key1), receive: new TestCipherState(key2) } satisfies Session,
    responder: { send: new TestCipherState(key2), receive: new TestCipherState(key1) } satisfies Session,
  }
}

/** Encode a ChannelMessage into the wire format (4-byte length prefix + protobuf). */
export function encodeWireMessage(channelId: string, ciphertext: Uint8Array, opts?: { close?: boolean, id?: number }): ArrayBuffer {
  const msg = create(ChannelMessageSchema, {
    protocolVersion: 1,
    channelId,
    ciphertext,
    flags: opts?.close ? 2 : 0,
    correlationId: BigInt(opts?.id ?? 0),
  })
  return frameBytes(toBinary(ChannelMessageSchema, msg)).buffer as ArrayBuffer
}

/**
 * Encode a wire message with an arbitrary uint64 correlation id, including ones
 * outside the range this client's own allocator can produce.
 */
export function encodeWireMessageWithBigIntId(channelId: string, ciphertext: Uint8Array, id: bigint): ArrayBuffer {
  const msg = create(ChannelMessageSchema, { protocolVersion: 1, channelId, ciphertext, correlationId: id })
  return frameBytes(toBinary(ChannelMessageSchema, msg)).buffer as ArrayBuffer
}

/** Encode a close notification. */
export function encodeCloseMessage(channelId: string): ArrayBuffer {
  return encodeWireMessage(channelId, new Uint8Array(), { close: true })
}

/** Parse a sent wire-format buffer back into a ChannelMessage. */
export function decodeWireMessage(buf: ArrayBuffer) {
  const framed = unframeBytes(new Uint8Array(buf))
  if (!framed.ok) {
    throw new Error(`decodeWireMessage: framing failure ${framed.failure.kind}`)
  }
  return fromBinary(ChannelMessageSchema, framed.payload)
}

/**
 * A channel's private per-request registries.
 *
 * Tests assert on their contents because a leak here has no public surface: a stranded
 * pending entry, an unreachable stream listener or an abandoned reassembly buffer just
 * sits in the map holding memory and a cap slot, and every public method keeps
 * answering exactly as it did before. The map IS the observable.
 */
export interface ChannelInternals {
  workerId: string
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

export function channelInternals(cm: ChannelManager, channelId: string): ChannelInternals {
  const ch = (cm as unknown as { channels: Map<string, ChannelInternals> }).channels.get(channelId)
  if (!ch) {
    throw new Error(`no channel ${channelId} on the manager`)
  }
  return ch
}

// ---- Mock WebSocket ----

type EventCallback = (...args: any[]) => void

export class MockWebSocket {
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

export const sessions = new Map<string, { initiator: Session, responder: Session }>()

export function clearSessions(): void {
  sessions.clear()
}

export function createMockTransport(
  mockWs: MockWebSocket,
  openOpts?: { maxMessageSize?: number | false },
): ChannelTransport {
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
      // maxMessageSize mirrors production negotiation unless a test asks to omit
      // it (so ChannelManagerOpts.testPayloadBudget / testReassembledCeiling can
      // act as the small-limit fallback used by size-gate tests).
      const result: {
        channelId: string
        handshakePayload: Uint8Array
        userId: string
        maxMessageSize?: number
      } = {
        channelId,
        handshakePayload: new Uint8Array(49904),
        userId: 'hub-authenticated-user',
      }
      if (openOpts?.maxMessageSize !== false)
        result.maxMessageSize = openOpts?.maxMessageSize ?? DEFAULT_MAX_MESSAGE_SIZE
      return result
    },
    async closeChannel(_channelId: string) {},
    createWebSocket(): ChannelSocket {
      return mockWs
    },
  }
}

// ---- Mock handshake functions (injected via ChannelManager DI) ----

export function mockHandshake1(_remoteX25519Pub: Uint8Array, _remoteMlkemPub: Uint8Array) {
  return {
    handshakeState: {} as any,
    message1: new Uint8Array(1616),
  }
}

export function mockHandshake2(_state: any, _message2: Uint8Array, _remoteSlhdsaPub: Uint8Array) {
  const entries = [...sessions.entries()]
  const lastEntry = entries.at(-1)
  if (!lastEntry)
    throw new Error('No session registered')
  return lastEntry[1].initiator
}

/**
 * The correlation id of the FIRST request a test issues after openTestChannel:
 * the open itself consumes id 1 on each channel for its session-verifying Ping.
 * Naming it once keeps the coupling in one place -- if the open ever round-trips
 * another RPC, only this changes.
 */
export const FIRST_TEST_REQUEST_ID = 2

/** Owns mgr/mockWs and the helpers that close over them for ChannelManager suites. */
export class ChannelManagerTestHarness {
  /** Same as the module export — kept on the class for suites that prefer `h.FIRST_TEST_REQUEST_ID`. */
  readonly FIRST_TEST_REQUEST_ID = FIRST_TEST_REQUEST_ID

  mockWs!: MockWebSocket
  mgr!: ChannelManager

  setup(): void {
    clearAllKeyPins()
    sessions.clear()
    this.mockWs = new MockWebSocket()
    this.mgr = new ChannelManager(createMockTransport(this.mockWs), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      idleRekeyIntervalMs: 0,
      installWakeListener: false,
      keyPins: acceptingKeyPins(),
    })
  }

  teardown(): void {
    this.mgr.closeAll()
  }

  /**
   * Flush pending microtasks so async code that chains multiple
   * resolved-promise awaits (like openChannel) progresses.
   */
  async flushMicrotasks() {
    for (let i = 0; i < 10; i++) {
      await Promise.resolve()
    }
  }

  /**
   * Answer the open-time Ping. openChannel round-trips a no-op Ping to prove the
   * E2EE session decrypts in both directions before it resolves, so a test worker
   * must reply to it or the open never completes. Decrypts the ping (advancing the
   * responder's receive nonce) and replies on its own correlation id.
   */
  simulatePingAccept(ws: MockWebSocket = this.mockWs, sessionMap = sessions) {
    const sentMsg = decodeWireMessage(ws.sent.at(-1)!)
    const pair = sessionMap.get(sentMsg.channelId)!
    const inner = fromBinary(InnerMessageSchema, pair.responder.receive.decrypt(sentMsg.ciphertext))
    expect(inner.kind.case).toBe('request')
    const resp = create(InnerRpcResponseSchema, { isError: false })
    const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
    const ciphertext = pair.responder.send.encrypt(toBinary(InnerMessageSchema, envelope))
    ws.simulateMessage(encodeWireMessage(sentMsg.channelId, ciphertext, { id: Number(sentMsg.correlationId) }))
  }

  /** Answer an in-band RekeyRequest the way the worker does (Receive.rekeyWithSecret → Ack → Send.rekeyWithSecret). */
  simulateRekeyAck(ws: MockWebSocket = this.mockWs, sessionMap = sessions) {
    const sentMsg = decodeWireMessage(ws.sent.at(-1)!)
    const pair = sessionMap.get(sentMsg.channelId)!
    const inner = fromBinary(InnerMessageSchema, pair.responder.receive.decrypt(sentMsg.ciphertext))
    expect(inner.kind.case).toBe('rekeyRequest')
    // Fresh-DH rekey: the test double ignores the secret bytes, but production
    // derives them from the request's dhPub + a responder ephemeral. Use a real
    // responder ephemeral pubkey in the Ack so the initiator's production DH
    // derivation (over real X25519) succeeds.
    const responderEph = generateRekeyEphemeral()
    const placeholderSecret = new Uint8Array(32)
    pair.responder.receive.rekeyWithSecret(placeholderSecret, null, true)
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'rekeyAck', value: create(RekeyAckSchema, { dhPub: responderEph.publicKey }) },
    })
    const ciphertext = pair.responder.send.encrypt(toBinary(InnerMessageSchema, envelope))
    pair.responder.send.rekeyWithSecret(placeholderSecret, null, false)
    ws.simulateMessage(encodeWireMessage(sentMsg.channelId, ciphertext, { id: Number(sentMsg.correlationId) }))
  }

  /** Rate-limit Reject: decrypt Request, leave CipherStates unchanged, reply Reject. */
  simulateRekeyReject(ws: MockWebSocket = this.mockWs, sessionMap = sessions, retryAfterMs = 0) {
    const sentMsg = decodeWireMessage(ws.sent.at(-1)!)
    const pair = sessionMap.get(sentMsg.channelId)!
    const inner = fromBinary(InnerMessageSchema, pair.responder.receive.decrypt(sentMsg.ciphertext))
    expect(inner.kind.case).toBe('rekeyRequest')
    const envelope = create(InnerMessageSchema, {
      kind: {
        case: 'rekeyReject',
        value: create(RekeyRejectSchema, retryAfterMs > 0 ? { retryAfterMs: BigInt(retryAfterMs) } : {}),
      },
    })
    const ciphertext = pair.responder.send.encrypt(toBinary(InnerMessageSchema, envelope))
    ws.simulateMessage(encodeWireMessage(sentMsg.channelId, ciphertext, { id: Number(sentMsg.correlationId) }))
  }

  async openTestChannel(workerId = 'w1'): Promise<string> {
    const openPromise = this.mgr.openChannel(workerId)
    // Flush microtasks so openChannel progresses through its awaits
    // and ensureWebSocket() registers the 'open' listener.
    await this.flushMicrotasks()
    this.mockWs.simulateOpen()
    await this.flushMicrotasks()
    // The open completes once the worker answers the session-verifying Ping.
    this.simulatePingAccept()
    return openPromise
  }

  sendResponseFromWorker(channelId: string, requestId: number, payload: Uint8Array) {
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
    this.mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  sendErrorResponseFromWorker(channelId: string, requestId: number, errorMessage: string) {
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
    this.mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  sendStreamMessageFromWorker(channelId: string, requestId: number, payload: Uint8Array) {
    const pair = sessions.get(channelId)!
    const msg = create(InnerStreamMessageSchema, {
      payload,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'stream', value: msg },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    this.mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  sendStreamEndFromWorker(channelId: string, requestId: number) {
    const pair = sessions.get(channelId)!
    const msg = create(InnerStreamMessageSchema, {
      end: true,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'stream', value: msg },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    this.mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  sendStreamErrorFromWorker(channelId: string, requestId: number, errorMessage: string) {
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
    this.mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }
}

// ---------------------------------------------------------------------------
// Identity-cipher helpers for concurrent-open / pooled-identity suites
// ---------------------------------------------------------------------------

/** Mock WebSocket that auto-opens on the next microtask (simulates TCP + upgrade). */
export class AutoOpenMockWebSocket extends EventTarget {
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

/**
 * Answer the open-time Ping on an identity-cipher mock channel. openChannel
 * round-trips a no-op Ping to prove the E2EE session decrypts in both directions
 * before it resolves, so a mock worker must reply or the open never completes.
 * The mock sessions encrypt as identity, so the wire message is built directly.
 * The Ping is each channel's first request, hence correlation id 1.
 */
export function simulatePingAcceptOnWs(ws: AutoOpenMockWebSocket, channelId: string) {
  const resp = create(InnerRpcResponseSchema, { isError: false })
  const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
  const plaintext = toBinary(InnerMessageSchema, envelope)

  const channelMsg = create(ChannelMessageSchema, {
    protocolVersion: 1,
    channelId,
    ciphertext: plaintext,
    correlationId: 1n,
  })
  const frame = frameBytes(toBinary(ChannelMessageSchema, channelMsg))
  ws.dispatchEvent(new MessageEvent('message', { data: frame.buffer as ArrayBuffer }))
}

/**
 * Fail the open-time Ping on an identity-cipher mock channel: the worker answers, but
 * with an error, i.e. the session is proven NOT to work.
 */
export function simulatePingErrorOnWs(ws: AutoOpenMockWebSocket, channelId: string, message: string) {
  const resp = create(InnerRpcResponseSchema, { isError: true, errorMessage: message, errorCode: 2 })
  const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
  const plaintext = toBinary(InnerMessageSchema, envelope)

  const channelMsg = create(ChannelMessageSchema, {
    protocolVersion: 1,
    channelId,
    ciphertext: plaintext,
    correlationId: 1n,
  })
  const frame = frameBytes(toBinary(ChannelMessageSchema, channelMsg))
  ws.dispatchEvent(new MessageEvent('message', { data: frame.buffer as ArrayBuffer }))
}

export function makeMockSession(): Session {
  // `satisfies Session` (not `as unknown as`) so a future CipherState method
  // addition fails to compile here instead of silently diverging the mock.
  return {
    send: {
      encrypt: (pt: Uint8Array) => pt,
      decrypt: (ct: Uint8Array) => ct,
      needsRekey: () => false,
      rekeyWithSecret: () => {},
      clearPrev: () => {},
      nonce: () => 0,
    },
    receive: {
      encrypt: (pt: Uint8Array) => pt,
      decrypt: (ct: Uint8Array) => ct,
      needsRekey: () => false,
      rekeyWithSecret: () => {},
      clearPrev: () => {},
      nonce: () => 0,
    },
  } satisfies Session
}

export function makeMockTransport(onCreateWs: () => AutoOpenMockWebSocket): ChannelTransport {
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
      maxMessageSize: DEFAULT_MAX_MESSAGE_SIZE,
    }),
    closeChannel: vi.fn().mockResolvedValue(undefined),
    createWebSocket: () => onCreateWs(),
  }
}

/** Advance real timers enough for the mock handshake and the WS auto-open to settle. */
export function settle(): Promise<void> {
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
export async function waitForPendingChannel(cm: ChannelManager, channelId: string): Promise<void> {
  const channels = (cm as unknown as { channels: Map<string, unknown> }).channels
  for (let i = 0; i < 400; i++) {
    if (channels.has(channelId))
      return
    await new Promise(r => setTimeout(r, 5))
  }
  throw new Error(`channel ${channelId} never reached its verification ping`)
}

export function makeCountingTransport(onCreateWs: () => AutoOpenMockWebSocket, userId: () => string = () => 'user-1'): ChannelTransport {
  const transport = makeMockTransport(onCreateWs)
  let channelCounter = 0
  transport.openChannel = vi.fn().mockImplementation(async () => ({
    channelId: `ch-${++channelCounter}`,
    handshakePayload: new Uint8Array(48),
    userId: userId(),
    maxMessageSize: DEFAULT_MAX_MESSAGE_SIZE,
  }))
  return transport
}

export function makeIdentityCipherManager(
  transport: ChannelTransport,
  opts?: { expectedUserId?: () => string | undefined, keyPins?: KeyPinStore },
): ChannelManager {
  return new ChannelManager(transport, {
    classicHandshake1: (_rs: Uint8Array) => ({ message1: new Uint8Array(48), handshakeState: {} as any }),
    classicHandshake2: (_hs: any, _payload: Uint8Array) => makeMockSession(),
    idleRekeyIntervalMs: 0,
    installWakeListener: false,
    keyPins: acceptingKeyPins(),
    ...opts,
  })
}
