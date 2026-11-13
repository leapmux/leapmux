import type { MessageInitShape } from '@bufbuild/protobuf'
import type { ChannelManagerOpts, ChannelSocket, ChannelTransport, WorkerKeyBundle } from './channel'
import type { SessionChannel } from './channelSession'
import type { SessionWorkerHandlers, SessionWorkerPort } from './channelSessionClient'
import type { ChannelSessionEndpointOptions } from './channelSessionEndpoint'
import type { InProcessSessionHost } from './channelSessionInProcess'
import type { KeyPinConfirmFn } from './keyPinStore'
import type { HandshakeState, Session } from './noise'
import type { HybridHandshakeState } from './noise-hybrid'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { afterEach, beforeEach, expect, vi } from 'vitest'
import { DEFAULT_MAX_MESSAGE_SIZE, MLKEM_PUBLIC_KEY_SIZE, SESSION_KEY_HARD_CEILING_MS, SESSION_KEY_MAX_AGE_MS, SLHDSA_PUBLIC_KEY_SIZE, X25519_PUBLIC_KEY_SIZE } from '~/generated/contracts/wire'
import {
  ChannelMessageSchema,
  EncryptionMode,
  InnerMessageSchema,
  InnerRpcResponseSchema,
  InnerStreamMessageSchema,
  RekeyAckSchema,
  RekeyRejectSchema,
} from '~/generated/proto/leapmux/v1/channel_pb'
import { blankClassicHandshakeState, blankHybridHandshakeState, createTestSession } from '~/test-support/channelSessionDoubles'
import { makeIdentitySession } from '~/test-support/identitySession'
import { ChannelManager } from './channel'
import { frameBytes, unframeBytes } from './channelFraming'
import { createInProcessSessionHost } from './channelSessionInProcess'
import { FIRST_REQUEST_ID } from './channelSessionProtocol'
import { clearAllKeyPins, KeyPinStore } from './keyPinStore'
import { generateRekeyEphemeral } from './noise-hybrid'
import { yieldToEventLoop } from './workSlices'

/** Accepting TOFU store for tests that pin on first use / accept a changed key. */
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

/** Whether `value` is a Uint8Array of any realm. */
function isByteArray(value: unknown): value is Uint8Array {
  return ArrayBuffer.isView(value) && Object.prototype.toString.call(value) === '[object Uint8Array]'
}

/**
 * Compare two Uint8Arrays by their bytes, whatever realm each comes from.
 *
 * Under the jsdom environment, a test builds its expected bytes in the realm of
 * jsdom, while the bytes that the in-process session host delivers come from
 * Node's realm: a MessagePort and structuredClone both build their clones there.
 * The two Uint8Array classes differ, so the default equality fails on equal
 * bytes. In a browser, the worker's bytes arrive in the page's own realm.
 */
function equalByteArrays(a: unknown, b: unknown): boolean | undefined {
  if (!isByteArray(a) || !isByteArray(b))
    return undefined
  if (a.byteLength !== b.byteLength)
    return false
  for (let i = 0; i < a.byteLength; i++) {
    if (a[i] !== b[i])
      return false
  }
  return true
}

expect.addEqualityTesters([equalByteArrays])

// ---- Channel session hosts ----
//
// The channel session (the Noise keys, encryption, decryption, the rekey) runs
// in a Web Worker in the browser. Here every test manager hosts it in process,
// behind a real MessageChannel pair (createInProcessSessionHost), so that a test
// runs the code that production runs, with its asynchronous delivery. A test
// awaits `settleChannelSessions()` wherever the browser would wait for the
// worker: before it reads the frames on the wire, and after it injects one.

/** One test manager's session host, and the ports that it spawned, latest last. */
interface TestSessionHost {
  host: InProcessSessionHost
  ports: SessionWorkerPort[]
}

/** Every session host of a test manager that the current test built. */
const hosts = new Set<TestSessionHost>()
/** The session host of each test manager. */
const hostOfManager = new WeakMap<ChannelManager, InProcessSessionHost>()
/** The failure handler of the latest spawned session worker (see failChannelSessionWorker). */
let latestFailure: ((message: string) => void) | undefined

/** The options of a test manager: the endpoint options of its in-process session host in place of the spawn. */
export type TestChannelManagerOpts = Omit<ChannelManagerOpts, 'spawnSessionWorker'> & {
  session?: ChannelSessionEndpointOptions
}

/**
 * A ChannelManager whose channel sessions run in an in-process host built from
 * `opts.session`. The host stays registered until the test ends, which
 * terminates it.
 */
export function newTestChannelManager(transport: ChannelTransport, opts: TestChannelManagerOpts = {}): ChannelManager {
  const { session, ...managerOpts } = opts
  const host = createInProcessSessionHost(session)
  const record: TestSessionHost = { host, ports: [] }
  hosts.add(record)
  const manager = new ChannelManager(transport, {
    ...managerOpts,
    spawnSessionWorker: () => {
      const port = host.spawn()
      record.ports.push(port)
      return {
        postMessage: (command, transfer) => port.postMessage(command, transfer),
        connect: (handlers: SessionWorkerHandlers) => {
          latestFailure = handlers.failure
          port.connect(handlers)
        },
        terminate: () => port.terminate(),
      }
    },
  })
  hostOfManager.set(manager, host)
  return manager
}

/** The in-process session host of a manager that newTestChannelManager built. */
export function sessionHostOf(manager: ChannelManager): InProcessSessionHost {
  const host = hostOfManager.get(manager)
  if (host === undefined)
    throw new Error('the manager has no test session host; build it with newTestChannelManager')
  return host
}

/** The most rounds that settleChannelSessions waits before it reports a session that never settles. */
export const MAX_SETTLE_ROUNDS = 1_000

/**
 * Resolve once no message is in flight between any test manager and its
 * session worker, in either direction. Each round yields to the event loop
 * first: a port delivers in a task of its own, and a vitest fake clock does not
 * hold it.
 *
 * A session request that waits for the peer, such as a rekey that waits for its
 * Ack, holds nothing in flight, so this returns while it waits.
 */
export async function settleChannelSessions(): Promise<void> {
  for (let round = 0; round < MAX_SETTLE_ROUNDS; round++) {
    await yieldToEventLoop()
    let idle = true
    for (const { host } of hosts) {
      if (host.inFlight() > 0)
        idle = false
    }
    if (idle)
      return
  }
  throw new Error(`the channel session still had messages in flight after ${MAX_SETTLE_ROUNDS} rounds`)
}

/** The session worker's record of `channelId`: its keys, its rekey state and its reassembly buffers. */
export function sessionStateOf(channelId: string): SessionChannel {
  for (const { host } of hosts) {
    const state = host.endpoint()?.sessionState(channelId)
    if (state !== undefined)
      return state
  }
  throw new Error(`no channel session ${channelId} in any test session host`)
}

/** Fail the latest spawned session worker with `message`, as a crash of a real Worker does. */
export function failChannelSessionWorker(message: string): void {
  if (latestFailure === undefined)
    throw new Error('no channel session worker was spawned')
  latestFailure(message)
}

afterEach(() => {
  // A test that did not close its manager leaves ports open, and an open port
  // keeps the test process alive.
  for (const { ports } of hosts) {
    for (const port of ports)
      port.terminate()
  }
  hosts.clear()
  latestFailure = undefined
})

/** Age a channel past SESSION_KEY_MAX_AGE_MS using the monotonic clock. */
export function agePastMaxAge(ch: { lastRekeyAt: number }): void {
  ch.lastRekeyAt = performance.now() - SESSION_KEY_MAX_AGE_MS - 1
}

/** Age a channel past SESSION_KEY_HARD_CEILING_MS using the monotonic clock. */
export function agePastHardCeiling(ch: { lastRekeyAt: number }): void {
  ch.lastRekeyAt = performance.now() - SESSION_KEY_HARD_CEILING_MS
}

// ---- Test helpers ----

/** Encode a ChannelMessage into the wire format (4-byte length prefix + protobuf). */
export function encodeWireMessage(channelId: string, ciphertext: Uint8Array, opts?: { close?: boolean, id?: number | bigint, flags?: number }): ArrayBuffer {
  const msg = create(ChannelMessageSchema, {
    channelId,
    ciphertext,
    flags: opts?.close
      ? 2
      : (opts?.flags ?? 0),
    correlationId: BigInt(opts?.id ?? 0),
  })
  return frameBytes(toBinary(ChannelMessageSchema, msg)).buffer as ArrayBuffer
}

/**
 * Encode a wire message with an arbitrary uint64 correlation id, including ones
 * outside the range this client's own allocator can produce.
 */
export function encodeWireMessageWithBigIntId(channelId: string, ciphertext: Uint8Array, id: bigint): ArrayBuffer {
  return encodeWireMessage(channelId, ciphertext, { id })
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
 * A channel's private per-request registries on the main thread.
 *
 * Tests assert on their contents because a leak here has no public surface: a stranded
 * pending entry or an unreachable stream listener just sits in the map holding memory,
 * and every public method keeps answering exactly as it did before. The map IS the
 * observable. The keys and the reassembly buffers live in the session worker: read
 * them through sessionStateOf.
 */
export interface ChannelInternals {
  workerId: string
  pendingRequests: Map<number, unknown>
  streamListeners: Map<number, unknown>
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
    // ArrayBuffer.isView, not instanceof: a frame from the in-process session
    // host is a Uint8Array of Node's realm under the jsdom environment.
    if (ArrayBuffer.isView(data)) {
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
          x25519PublicKey: new Uint8Array(X25519_PUBLIC_KEY_SIZE),
          mlkemPublicKey: new Uint8Array(MLKEM_PUBLIC_KEY_SIZE),
          slhdsaPublicKey: new Uint8Array(SLHDSA_PUBLIC_KEY_SIZE),
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

export function mockHandshake1(_remoteX25519Pub: Uint8Array, _remoteMlkemPub: Uint8Array): { handshakeState: HybridHandshakeState, message1: Uint8Array } {
  return {
    handshakeState: blankHybridHandshakeState(),
    message1: new Uint8Array(1616),
  }
}

export async function mockHandshake2(_state: HybridHandshakeState, _message2: Uint8Array, _remoteSlhdsaPub: Uint8Array): Promise<Session> {
  const entries = [
    ...sessions.entries(),
  ]
  const lastEntry = entries.at(-1)
  if (!lastEntry)
    throw new Error('No session registered')
  return lastEntry[1].initiator
}

/**
 * The correlation id of the FIRST request a test issues after openTestChannel:
 * the open itself consumes FIRST_REQUEST_ID on each channel for its
 * session-verifying Ping. Naming it once keeps the coupling in one place -- if the
 * open ever round-trips another RPC, only this changes.
 */
export const FIRST_TEST_REQUEST_ID = FIRST_REQUEST_ID + 1

/** Owns mgr/mockWs and the helpers that close over them for ChannelManager suites. */
export class ChannelManagerTestHarness {
  /** Same as the module export — kept on the class for suites that prefer `h.FIRST_TEST_REQUEST_ID`. */
  readonly FIRST_TEST_REQUEST_ID = FIRST_TEST_REQUEST_ID

  mockWs!: MockWebSocket
  /** The transport of `mgr`, so that a test can spy on the Hub calls. */
  transport!: ChannelTransport
  mgr!: ChannelManager

  setup(): void {
    clearAllKeyPins()
    sessions.clear()
    this.mockWs = new MockWebSocket()
    this.transport = createMockTransport(this.mockWs)
    this.mgr = newTestChannelManager(this.transport, {
      session: {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        idleRekeyIntervalMs: 0,
      },
      installWakeListener: false,
      keyPins: acceptingKeyPins(),
    })
  }

  teardown(): void {
    this.mgr.closeAll()
  }

  /** Wait until the channel sessions have taken every message (see settleChannelSessions). */
  settle(): Promise<void> {
    return settleChannelSessions()
  }

  /**
   * Answer the open-time Ping. openChannel round-trips a no-op Ping to prove the
   * E2EE session decrypts in both directions before it resolves, so a test worker
   * must reply to it or the open never completes. Decrypts the ping (advancing the
   * responder's receive nonce) and replies on its own correlation id.
   */
  async simulatePingAccept(ws: MockWebSocket = this.mockWs, sessionMap = sessions) {
    await settleChannelSessions()
    const sentMsg = decodeWireMessage(ws.sent.at(-1)!)
    const pair = sessionMap.get(sentMsg.channelId)!
    const inner = fromBinary(InnerMessageSchema, pair.responder.receive.decrypt(sentMsg.ciphertext))
    expect(inner.kind.case).toBe('request')
    const resp = create(InnerRpcResponseSchema, {})
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'response', value: resp },
    })
    const ciphertext = pair.responder.send.encrypt(toBinary(InnerMessageSchema, envelope))
    ws.simulateMessage(encodeWireMessage(sentMsg.channelId, ciphertext, {
      id: Number(sentMsg.correlationId),
    }))
    await settleChannelSessions()
  }

  /** Answer an in-band RekeyRequest the way the worker does (Receive.rekeyWithSecret → Ack → Send.rekeyWithSecret). */
  async simulateRekeyAck(ws: MockWebSocket = this.mockWs, sessionMap = sessions) {
    await settleChannelSessions()
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
      kind: {
        case: 'rekeyAck',
        value: create(RekeyAckSchema, { dhPub: responderEph.publicKey }),
      },
    })
    const ciphertext = pair.responder.send.encrypt(toBinary(InnerMessageSchema, envelope))
    pair.responder.send.rekeyWithSecret(placeholderSecret, null, false)
    ws.simulateMessage(encodeWireMessage(sentMsg.channelId, ciphertext, {
      id: Number(sentMsg.correlationId),
    }))
    await settleChannelSessions()
  }

  /** Rate-limit Reject: decrypt Request, leave CipherStates unchanged, reply Reject. */
  async simulateRekeyReject(ws: MockWebSocket = this.mockWs, sessionMap = sessions, retryAfterMs = 0) {
    await settleChannelSessions()
    const sentMsg = decodeWireMessage(ws.sent.at(-1)!)
    const pair = sessionMap.get(sentMsg.channelId)!
    const inner = fromBinary(InnerMessageSchema, pair.responder.receive.decrypt(sentMsg.ciphertext))
    expect(inner.kind.case).toBe('rekeyRequest')
    const envelope = create(InnerMessageSchema, {
      kind: {
        case: 'rekeyReject',
        value: create(RekeyRejectSchema, retryAfterMs > 0
          ? {
              retryAfterMs: BigInt(retryAfterMs),
            }
          : {}),
      },
    })
    const ciphertext = pair.responder.send.encrypt(toBinary(InnerMessageSchema, envelope))
    ws.simulateMessage(encodeWireMessage(sentMsg.channelId, ciphertext, {
      id: Number(sentMsg.correlationId),
    }))
    await settleChannelSessions()
  }

  async openTestChannel(workerId = 'w1'): Promise<string> {
    const openPromise = this.mgr.openChannel(workerId)
    // Let the open run its handshake in the session worker, until
    // ensureWebSocket() registers the 'open' listener.
    await settleChannelSessions()
    this.mockWs.simulateOpen()
    await settleChannelSessions()
    // The open completes once the worker answers the session-verifying Ping.
    await this.simulatePingAccept()
    return openPromise
  }

  /** Inject one framed message from the worker, and wait until the session worker took it. */
  private async injectFromWorker(channelId: string, requestId: number, envelope: MessageInitShape<typeof InnerMessageSchema>): Promise<void> {
    const pair = sessions.get(channelId)!
    const plaintext = toBinary(InnerMessageSchema, create(InnerMessageSchema, envelope))
    const ciphertext = pair.responder.send.encrypt(plaintext)
    this.mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
    await settleChannelSessions()
  }

  async sendResponseFromWorker(channelId: string, requestId: number, payload: Uint8Array) {
    await this.injectFromWorker(channelId, requestId, {
      kind: {
        case: 'response',
        value: create(InnerRpcResponseSchema, {
          result: { case: 'payload', value: payload },
        }),
      },
    })
  }

  async sendErrorResponseFromWorker(channelId: string, requestId: number, errorMessage: string) {
    await this.injectFromWorker(channelId, requestId, {
      kind: {
        case: 'response',
        value: create(InnerRpcResponseSchema, {
          result: {
            case: 'error',
            value: { code: 2, message: errorMessage },
          },
        }),
      },
    })
  }

  async sendStreamMessageFromWorker(channelId: string, requestId: number, payload: Uint8Array) {
    await this.injectFromWorker(channelId, requestId, {
      kind: {
        case: 'stream',
        value: create(InnerStreamMessageSchema, { payload }),
      },
    })
  }

  async sendStreamEndFromWorker(channelId: string, requestId: number) {
    await this.injectFromWorker(channelId, requestId, {
      kind: {
        case: 'stream',
        value: create(InnerStreamMessageSchema, { end: true }),
      },
    })
  }

  async sendStreamErrorFromWorker(channelId: string, requestId: number, errorMessage: string) {
    await this.injectFromWorker(channelId, requestId, {
      kind: {
        case: 'stream',
        value: create(InnerStreamMessageSchema, {
          error: { message: errorMessage },
        }),
      },
    })
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
 * The Ping is each channel's first request, hence correlation id FIRST_REQUEST_ID.
 * Resolves once the session worker took the reply.
 */
export async function simulatePingAcceptOnWs(ws: AutoOpenMockWebSocket, channelId: string) {
  await simulatePingResponseOnWs(ws, channelId, {})
}

/**
 * Fail the open-time Ping on an identity-cipher mock channel: the worker answers, but
 * with an error, i.e. the session is proven NOT to work.
 */
export async function simulatePingErrorOnWs(ws: AutoOpenMockWebSocket, channelId: string, message: string) {
  await simulatePingResponseOnWs(ws, channelId, {
    result: {
      case: 'error',
      value: { code: 2, message },
    },
  })
}

async function simulatePingResponseOnWs(
  ws: AutoOpenMockWebSocket,
  channelId: string,
  response: MessageInitShape<typeof InnerRpcResponseSchema>,
): Promise<void> {
  const resp = create(InnerRpcResponseSchema, response)
  const envelope = create(InnerMessageSchema, {
    kind: { case: 'response', value: resp },
  })
  const plaintext = toBinary(InnerMessageSchema, envelope)
  ws.dispatchEvent(new MessageEvent('message', {
    data: encodeWireMessage(channelId, plaintext, { id: FIRST_REQUEST_ID }),
  }))
  await settleChannelSessions()
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

/**
 * Wait until `channelId`'s open has reached its verification Ping, which is the point a
 * mock worker can answer it.
 *
 * Polls the manager rather than sleeping a fixed interval because how long an open takes
 * to get here is genuinely variable: a key-mismatch open awaits the user prompt and a
 * dynamic import('./fingerprint'), which under a loaded full suite comfortably outruns a
 * 10ms sleep. Answering a ping that has not been sent yet leaves the open hanging until
 * the test times out. The channel is inserted and the ping posted to the session worker
 * in one synchronous step, so observing the channel at all means that a reply injected
 * now reaches the worker after the ping: the port keeps the order.
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
  return newTestChannelManager(transport, {
    session: {
      classicHandshake1: (_rs: Uint8Array) => ({
        message1: new Uint8Array(48),
        handshakeState: blankClassicHandshakeState(),
      }),
      classicHandshake2: (_hs: HandshakeState, _payload: Uint8Array) => makeIdentitySession(),
      idleRekeyIntervalMs: 0,
    },
    installWakeListener: false,
    keyPins: acceptingKeyPins(),
    ...opts,
  })
}
