/**
 * Encrypted channel manager for E2EE communication with Workers.
 *
 * Manages the lifecycle of encrypted channels:
 *   1. Fetch Worker's handshake params (keys + encryption mode) via ChannelTransport.getWorkerHandshakeParams
 *   2. Check key pinning (TOFU model) — prompt user on mismatch
 *   3. Perform Noise_NK handshake via ChannelTransport.openChannel
 *   4. Connect a single shared WebSocket relay for all encrypted traffic
 *   5. Round-trip a no-op Ping to prove the session decrypts in both directions
 *   6. Encrypt/decrypt ChannelMessages using per-channel Noise sessions
 *
 * Platform-specific RPC and WebSocket creation is abstracted behind the
 * ChannelTransport interface, allowing the same ChannelManager to work
 * in both browser and Node.js/test environments.
 *
 * ChannelManager is the public facade / thin coordinator. Extracted seams:
 *   - reassembler.ts — chunk reassembly
 *   - keyPinStore.ts — TOFU key pinning
 *   - channelRpc.ts — RPC/stream multiplexer
 *   - channelRelay.ts — shared WebSocket + framing
 *   - channelFraming.ts — length-prefix encode/decode
 *   - channelSession.ts — Noise handshake / encrypt / decrypt / in-band rekey
 *   - channelPool.ts — channel map, open dedup, identity reuse
 *   - channelOpen.ts — pin → handshake → verify → commit open path
 *   - channelInbound.ts — decrypt / reassemble / InnerMessage dispatch
 *
 * See https://github.com/leapmux/leapmux/issues/292.
 */
import type { MessageInitShape, MessageShape } from '@bufbuild/protobuf'
import type { GenMessage } from '@bufbuild/protobuf/codegenv2'
import type { ChannelSocket } from './channelRelay'
import type { PendingRequest, StreamListener } from './channelRpc'
import type { ChannelSessionOpts } from './channelSession'
import type { Session } from './noise'
import type { Reassembler } from './reassembler'
import type { WorkerKeyBundle } from './workerKeyBundle'
import type { FatalCloseInfo } from './wsCloseCodes'
import type { ChannelMessage, EncryptionMode, HubControlFrame, InnerRpcResponse, InnerStreamMessage } from '~/generated/leapmux/v1/channel_pb'
import { create, fromBinary, toBinary, toJsonString } from '@bufbuild/protobuf'
import {
  HubControlFrameSchema,
} from '~/generated/leapmux/v1/channel_pb'
import { abortError, ChannelError } from './channelError'
import { ChannelInbound } from './channelInbound'
import { ChannelOpen } from './channelOpen'
import { ChannelPool } from './channelPool'
import { ChannelRelay } from './channelRelay'
import { ChannelRpcMux } from './channelRpc'
import { ChannelSession } from './channelSession'
import { formatErrorMessage } from './errors'
import { fatalCloseError } from './fatalCloseMessage'
import { KeyPinStore } from './keyPinStore'
import { createLogger } from './logger'

export type { ChannelErrorSource } from './channelError'
export { abortError, ChannelError } from './channelError'
export type { ChannelSocket } from './channelRelay'
export {
  MIN_REKEY_INTERVAL_MS,
  SESSION_KEY_HARD_CEILING_MS,
  SESSION_KEY_MAX_AGE_MS,
} from './channelSession'
export type { KeyPinDecision } from './keyPinStore'
export { clearAllKeyPins, clearKeyPin, KeyPinRejectedError, KeyPinStore } from './keyPinStore'
export type { WorkerKeyBundle } from './workerKeyBundle'

const log = createLogger('channel')

// safeCall invokes a user-supplied listener callback and swallows any throw so
// one throwing listener cannot break the iteration that notifies the rest. The
// state, error, and stream-teardown loops all fan out to consumer callbacks; an
// uncaught throw in one would leave later listeners unnotified and teardown
// half-done (a leaked Hub-side channel, a stranded pending request). It mirrors
// the per-callback isolation handleHubControl already applies to frame
// callbacks.
function safeCall(fn: () => void, description: string): void {
  try {
    fn()
  }
  catch (err) {
    log.warn('listener threw; continuing with the remaining listeners', { what: description, error: err })
  }
}

/**
 * The no-op inner RPC openChannel round-trips to prove the E2EE session decrypts
 * in both directions before returning the channel. Must match the worker's
 * registered handler — the Go side keeps this name in `channelwire.PingMethod`,
 * and both sides pin it to the cross-language fixture
 * (testdata/channelwire_limits.json) so a rename on one reddens CI here instead
 * of desyncing the open-time Ping the other end expects.
 */
export const PING_METHOD = 'Ping'

/** Idle rekey poll interval, matching Go tunnel rekeyIdleLoop. */
const DEFAULT_IDLE_REKEY_INTERVAL_MS = 60_000

/** Worker key bundle returned by transport. */
export interface ChannelTransport {
  /**
   * Fetches the public key material and live encryption mode in one round
   * trip. Both are needed before every OpenChannel, so they travel together.
   */
  getWorkerHandshakeParams: (workerId: string) => Promise<{ keys: WorkerKeyBundle, encryptionMode: EncryptionMode }>
  openChannel: (workerId: string, handshakePayload: Uint8Array) => Promise<{ channelId: string, handshakePayload: Uint8Array, userId: string, maxMessageSize?: number }>
  closeChannel: (channelId: string) => Promise<void>
  createWebSocket: () => ChannelSocket
}

interface ActiveChannel {
  channelId: string
  workerId: string
  session: Session
  /**
   * The identity the Hub authenticated this channel's open as. Recorded because
   * channels are POOLED across in-band rekeys: the open-time cross-check only
   * proves who the page was when the channel was created, so getOrOpenChannel
   * re-compares this before handing a pooled channel out (see its identity check).
   * Age rotation uses rekey; past SESSION_KEY_HARD_CEILING_MS the pool closes and
   * re-handshakes instead of serving under the over-age key.
   */
  userId: string
  /**
   * Negotiated application payload budget from OpenChannelResponse.max_message_size.
   * Clients never configure this; open refuses a missing/out-of-bounds value
   * (except the test-only ChannelManagerOpts payload/reassembled fallback).
   */
  maxMessageSize: number
  /**
   * Send/reassembly ceiling: maxReassembledMessageSize(maxMessageSize), or the
   * test-only testReassembledCeiling when the mock omits negotiation.
   */
  maxReassembledMessageSize: number
  pendingRequests: Map<number, PendingRequest>
  streamListeners: Map<number, StreamListener>
  reassembly: Reassembler
  nextRequestId: number
  /**
   * Lifecycle state, gating pool handout -- a single field rather than separate
   * `verified`/`closed` booleans, whose fourth combination (verified AND closed)
   * no gate ever distinguished:
   *   - 'opening': present in the pool so the open-time Ping's reply can route
   *     (handleMessage looks the channel up by id), but NOT open for business.
   *     hasOpenChannel and getOrOpenChannel skip it, so a racing caller waits on
   *     the open (openingChannels dedups it onto the same one) instead of being
   *     handed a session that may yet prove dead.
   *   - 'verified': the Ping round-tripped, so the channel may be served.
   *   - 'closed': torn down. Every path that sets this also deletes the channel
   *     from the pool, so a channel is only ever observed 'closed' transiently
   *     by a caller mid-teardown.
   */
  state: 'opening' | 'verified' | 'closed'
  /**
   * Handshake / last successful in-band rekey time from performance.now()
   * (monotonic). Used for age / hard-ceiling / reject-backoff policy so NTP
   * wall-clock steps cannot stretch or shrink key lifetime.
   */
  lastRekeyAt: number
  /** After Reject, suppress age-only retries until this performance.now() value. */
  rekeyNotBefore: number
  /** Non-null while waiting for RekeyAck / RekeyReject. */
  rekeyWait: ((accepted: boolean, retryAfterMs: number) => void) | null
  rekeyClear: (() => void) | null
  rekeyAbort: (() => void) | null
  /** Correlation id of the in-flight RekeyRequest (so Ack/Reject are not dropped as unknown). */
  rekeyRequestId: number | null
  /** Chains concurrent ensureRekeyed callers on this channel. */
  rekeyChain: Promise<void>
  /** Worker ML-KEM pub retained for rekey encapsulation; empty in classic mode. */
  workerMlkemPub: Uint8Array
  /** In-flight rekey initiator material; null when no rekey is in progress. */
  rekeyMaterial: { ePriv: Uint8Array, mlkemSS: Uint8Array | null } | null
}

/**
 * Fallback RPC call timeout in milliseconds, used only when the owner
 * doesn't inject a `rpcTimeoutFn`. Must be larger than the worker's own
 * `apiTimeoutSeconds` context deadline (10s default) so the worker has time
 * to respond with DeadlineExceeded before this client-side timer fires.
 * 15_000 == 10s × the 1.5× multiplier applied by ~/api/transport.
 */
const FALLBACK_RPC_TIMEOUT_MS = 15_000

/** Optional overrides for testing (dependency injection). */
export interface ChannelManagerOpts extends ChannelSessionOpts {
  /**
   * Test-only application payload budget used when the transport's openChannel
   * omits maxMessageSize. Production opens always take the negotiated payload
   * from the Hub. Pair with testReassembledCeiling, or omit the ceiling to
   * derive it via maxReassembledMessageSize(testPayloadBudget).
   */
  testPayloadBudget?: number
  /**
   * Test-only reassembled/send-gate ceiling when openChannel omits
   * maxMessageSize. If set without testPayloadBudget, the payload budget
   * defaults to the same value (tiny size-gate tests that need both ceilings
   * equal). If omitted while testPayloadBudget is set, the ceiling is
   * derived as maxReassembledMessageSize(payload).
   */
  testReassembledCeiling?: number
  /**
   * WebSocket open timeout in milliseconds (default 10_000). Tests that
   * exercise the timeout path pass a small value and use real timers —
   * bun's fake-timer clock does not reliably fire this setTimeout when the
   * open path has several awaits ahead of ensureWebSocket.
   */
  wsOpenTimeoutMs?: number
  /**
   * Default timeout for individual RPC calls in milliseconds. Resolved
   * lazily on every call so callers (typically ~/api/workerRpc) can forward
   * the current frontend-multiplied deadline from `loadTimeouts()`.
   */
  rpcTimeoutFn?: () => number
  /**
   * The identity this page believes it is authenticated as, resolved lazily on
   * every open. When it returns a value that disagrees with the identity the Hub
   * authenticated the open as, the open fails.
   *
   * This is a CROSS-CHECK, not a source of identity: the Hub's answer is always
   * authoritative and is never overridden by this. What it catches is the two
   * silently diverging — a tab rendered as user A whose shared cookie jar has since
   * been re-authenticated as B (a logout/login in another tab, an impersonation
   * switch, an admin "view as") opens a channel the Hub authenticates as B, and A's
   * UI then drives B's session on every worker B can reach. Comparing is not
   * asserting: a stale local id can still never speak for the channel, the open just
   * fails loudly instead of proceeding on a disagreement the page cannot see.
   *
   * Returns undefined when the page has no expectation (before auth resolves), which
   * skips the check.
   */
  expectedUserId?: () => string | undefined
  /**
   * Idle rekey poll interval in ms (default 60_000, matching Go). Set to 0 to
   * disable the idle timer (tests that only exercise send-path rekey).
   */
  idleRekeyIntervalMs?: number
  /**
   * Install document visibility / pageshow wake listeners that force a rekey /
   * hard-ceiling check after OS suspend. Default true when `document` exists.
   * Tests that drive wake via checkChannelsAfterWake pass false.
   */
  installWakeListener?: boolean
  /**
   * TOFU key-pinning policy. Defaults to a fail-closed store (mismatch rejects)
   * when omitted; production wires a store whose prompt is registered by AppShell.
   */
  keyPins?: KeyPinStore
}

/**
 * ChannelManager manages encrypted E2EE channels to Workers.
 *
 * It coordinates the extracted transport / session / RPC-multiplexer / pool
 * seams, and owns open/verify ordering (pin → handshake → `'opening'` → Ping →
 * commit → `'verified'`).
 */
export class ChannelManager {
  private transport: ChannelTransport
  private pool = new ChannelPool<ActiveChannel>()
  private relay: ChannelRelay
  private session: ChannelSession
  private rpc: ChannelRpcMux
  private open: ChannelOpen<ActiveChannel>
  private inbound: ChannelInbound<ActiveChannel>
  private testPayloadBudget: number | undefined
  private testReassembledCeiling: number | undefined
  private rpcTimeoutFn: () => number
  private expectedUserIdFn: () => string | undefined
  private keyPins: KeyPinStore
  private idleRekeyIntervalMs: number
  private idleRekeyTimer: ReturnType<typeof setInterval> | null = null
  private wakeCleanup: (() => void) | null = null

  private stateListeners = new Set<() => void>()
  private errorListeners = new Set<(workerId: string, error: ChannelError) => void>()
  private hubControlListeners = new Set<(frame: HubControlFrame) => void>()
  private fatalCloseListeners = new Set<(info: FatalCloseInfo) => void>()

  /**
   * Test seam: channel.*.test.ts reaches through `(mgr as any).channels`. The live
   * map lives on ChannelPool; this getter keeps that access path working.
   */
  private get channels(): Map<string, ActiveChannel> {
    return this.pool.asMap()
  }

  constructor(transport: ChannelTransport, opts?: ChannelManagerOpts) {
    this.transport = transport
    this.testPayloadBudget = opts?.testPayloadBudget
    this.testReassembledCeiling = opts?.testReassembledCeiling
    this.rpcTimeoutFn = opts?.rpcTimeoutFn ?? (() => FALLBACK_RPC_TIMEOUT_MS)
    this.expectedUserIdFn = opts?.expectedUserId ?? (() => undefined)
    this.idleRekeyIntervalMs = opts?.idleRekeyIntervalMs ?? DEFAULT_IDLE_REKEY_INTERVAL_MS
    this.keyPins = opts?.keyPins ?? new KeyPinStore({ confirmKeyPin: async () => 'reject' })

    this.relay = new ChannelRelay({
      createWebSocket: () => this.transport.createWebSocket(),
      wsOpenTimeoutMs: opts?.wsOpenTimeoutMs ?? 10_000,
      onFrame: (channelId, msg) => this.inbound.handleMessage(channelId, msg),
      onHubControl: msg => this.handleHubControl(msg),
      onCloseDrain: (successorDialing, fatal) => this.handleRelayCloseDrain(successorDialing, fatal),
      onFatalClose: info => this.notifyFatalClose(info),
    })

    this.session = new ChannelSession({
      sendToWire: buf => this.relay.send(buf),
      closeChannel: channelId => this.closeChannel(channelId),
      onSendFailure: (ch, err) => this.onSendFailure(ch as ActiveChannel, err),
    }, {
      handshake1: opts?.handshake1,
      handshake2: opts?.handshake2,
      classicHandshake1: opts?.classicHandshake1,
      classicHandshake2: opts?.classicHandshake2,
    })

    this.rpc = new ChannelRpcMux({
      send: (ch, plaintext, requestId) => this.session.sendEncryptedMessage(ch as ActiveChannel, plaintext, requestId),
      onSendFailure: (ch, err) => this.onSendFailure(ch as ActiveChannel, err),
      rpcTimeoutFn: () => this.rpcTimeoutFn(),
      notifyError: (workerId, error) => this.notifyError(workerId, error),
      safeCall,
    })

    this.open = new ChannelOpen({
      transport: this.transport,
      keyPins: this.keyPins,
      session: this.session,
      relay: this.relay,
      pool: this.pool,
      expectedUserId: () => this.expectedUserIdFn(),
      testPayloadBudget: this.testPayloadBudget,
      testReassembledCeiling: this.testReassembledCeiling,
      verifySession: ch => this.verifySession(ch),
      evictGhost: (channelId, reason) => this.evictGhost(channelId, reason),
      notifyStateChange: () => this.notifyStateChange(),
    })

    this.inbound = new ChannelInbound({
      getChannel: id => this.pool.get(id),
      session: this.session,
      rpc: this.rpc,
      closeChannel: (id) => { void this.closeChannel(id) },
      forgetClosedChannel: (channelId) => {
        this.pool.delete(channelId)
        this.stopIdleRekeyTimerIfEmpty()
        this.notifyStateChange()
      },
    })

    const installWake = opts?.installWakeListener ?? (typeof document !== 'undefined')
    if (installWake)
      this.installWakeListener()
  }

  onStateChange(cb: () => void): () => void {
    this.stateListeners.add(cb)
    return () => {
      this.stateListeners.delete(cb)
    }
  }

  onChannelError(cb: (workerId: string, error: ChannelError) => void): () => void {
    this.errorListeners.add(cb)
    return () => {
      this.errorListeners.delete(cb)
    }
  }

  onHubControl(cb: (frame: HubControlFrame) => void): () => void {
    this.hubControlListeners.add(cb)
    return () => {
      this.hubControlListeners.delete(cb)
    }
  }

  /**
   * The relay hit a close no redial can recover from — the hub refused this
   * connection (auth expiry/revocation, or the per-user connection cap).
   *
   * Distinct from onChannelError, which fires per worker for a recoverable
   * failure the caller retries: this fires once, means "stop", and is the only
   * signal carrying WHY. Without it a cap refusal is indistinguishable from a
   * flaky network, which is what let the app advise a reload that could only
   * produce another refusal.
   */
  onFatalClose(cb: (info: FatalCloseInfo) => void): () => void {
    this.fatalCloseListeners.add(cb)
    return () => {
      this.fatalCloseListeners.delete(cb)
    }
  }

  /**
   * The terminal close the relay has latched, or null while it is dialable.
   *
   * For a redial loop deciding whether to arm a timer at all. `onFatalClose`
   * only helps a caller that was listening at the moment it fired; this answers
   * the same question at any later point, which is what a loop reached through
   * some other path (a stream that ended on its own) needs.
   */
  fatalCloseInfo(): FatalCloseInfo | null {
    return this.relay.fatalCloseInfo()
  }

  private installWakeListener(): void {
    if (typeof document === 'undefined')
      return
    const onVisible = () => {
      if (typeof document !== 'undefined' && document.visibilityState !== 'visible')
        return
      this.checkChannelsAfterWake()
    }
    const onPageShow = (ev: PageTransitionEvent) => {
      // bfcache restore: pageshow with persisted=true; also cover plain visibility.
      if (ev.persisted || (typeof document !== 'undefined' && document.visibilityState === 'visible'))
        this.checkChannelsAfterWake()
    }
    document.addEventListener('visibilitychange', onVisible)
    if (typeof window !== 'undefined')
      window.addEventListener('pageshow', onPageShow)
    this.wakeCleanup = () => {
      document.removeEventListener('visibilitychange', onVisible)
      if (typeof window !== 'undefined')
        window.removeEventListener('pageshow', onPageShow)
    }
  }

  checkChannelsAfterWake(): void {
    for (const ch of this.pool.values()) {
      if (ch.state !== 'verified')
        continue
      if (this.session.needsRekeyGate(ch)) {
        void this.session.ensureRekeyed(ch).catch((err) => {
          log.warn('wake/idle rekey failed', {
            channel_id: ch.channelId,
            error: err instanceof Error ? err.message : formatErrorMessage(err),
          })
        })
      }
    }
  }

  private startIdleRekeyTimer(): void {
    if (this.idleRekeyIntervalMs <= 0 || this.idleRekeyTimer !== null)
      return
    this.idleRekeyTimer = setInterval(() => this.checkChannelsAfterWake(), this.idleRekeyIntervalMs)
  }

  private stopIdleRekeyTimerIfEmpty(): void {
    if (this.pool.size > 0 || this.idleRekeyTimer === null)
      return
    clearInterval(this.idleRekeyTimer)
    this.idleRekeyTimer = null
  }

  /**
   * Whether a usable channel to this worker already exists, for connection-
   * indicator callers. Skips `'opening'` and identity-drifted channels.
   *
   * A channel whose Hub-authenticated identity has drifted from who this page now is
   * (a logout/login or impersonation switch left a pooled channel authenticated as
   * another user) does NOT count either: getOrOpenChannel would reject and reopen it
   * on the next RPC, so reporting "connected" for it would show a live link the
   * current user cannot actually use as themselves. Age and soft-nonce rotation use
   * in-band rekey and are deliberately NOT treated as "not open" here -- a channel
   * mid-rekey or a minute past the age cap is still "connected" for indicator purposes.
   */
  hasOpenChannel(workerId: string): boolean {
    return this.pool.hasOpenChannel(workerId, this.expectedUserIdFn())
  }

  private notifyStateChange(): void {
    for (const cb of this.stateListeners) {
      safeCall(() => cb(), 'state change listener')
    }
  }

  private notifyError(workerId: string, error: ChannelError): void {
    for (const cb of this.errorListeners) {
      safeCall(() => cb(workerId, error), 'error listener')
    }
  }

  private notifyFatalClose(info: FatalCloseInfo): void {
    for (const cb of this.fatalCloseListeners) {
      safeCall(() => cb(info), 'fatal close listener')
    }
  }

  /**
   * Open an encrypted channel to a Worker.
   * Performs the Noise_NK handshake, key pinning check, connects the shared
   * WebSocket relay, and verifies the session with a Ping round trip.
   *
   * Single-flighted per worker so parallel direct callers cannot register two
   * Hub channel IDs. Unlike getOrOpenChannel, does not reuse an existing
   * verified channel (callers that need force-reopen after a key change use this).
   */
  async openChannel(workerId: string): Promise<string> {
    return this.pool.dedupeOpen(workerId, () => this.openChannelUncached(workerId))
  }

  /**
   * Open path without pool reuse / single-flight — only called as the factory
   * inside ChannelPool.getOrOpenChannel (which already owns the dedup lock).
   */
  private async openChannelUncached(workerId: string): Promise<string> {
    // Fail before the Hub round-trip, not just before the dial. The open path
    // is openChannel RPC -> WS dial -> closeChannel rollback, so a latch that
    // only guarded the dial would still let every retry cost the hub two RPCs.
    // Both redial loops above (workerPrivateEvents' `while (!stopped)` and
    // useWatchEventsStreams' scheduleReconnect) retry on any error forever, so
    // "refused" has to be answered here or it becomes a permanent load.
    const fatal = this.relay.fatalCloseInfo()
    if (fatal) {
      throw fatalCloseError(fatal)
    }
    return this.open.openUncached(workerId)
  }

  /**
   * Round-trip Ping to prove the E2EE session before marking the channel verified.
   * On failure the channel is evicted here so it never leaves this method verified.
   */
  private async verifySession(channel: ActiveChannel): Promise<void> {
    try {
      await this.call(channel.channelId, PING_METHOD, new Uint8Array())
    }
    catch (err) {
      const failure = err instanceof ChannelError
        ? err
        : new ChannelError('transport', `verify channel session: ${formatErrorMessage(err)}`)
      this.evictGhost(channel.channelId, failure)
      throw failure
    }
    channel.state = 'verified'
    this.startIdleRekeyTimer()
  }

  /**
   * Remove a stranded channel from the pool and fail every request that slipped
   * onto it, once. It is the single teardown for a channel that entered
   * the pool but must not be served: the ping-failure and post-verification
   * failure paths of openChannel both route through here so they cannot drift.
   */
  private evictGhost(channelId: string, err: ChannelError): void {
    const ghost = this.pool.get(channelId)
    if (!ghost)
      return
    ghost.state = 'closed'
    this.pool.delete(channelId)
    this.stopIdleRekeyTimerIfEmpty()
    this.rpc.drainChannel(ghost, err, 'error')
  }

  /** Close an encrypted channel (does not close the shared WebSocket). */
  async closeChannel(
    channelId: string,
    opts?: { streamTermination?: 'end' | 'error', reason?: ChannelError },
  ): Promise<void> {
    const ch = this.pool.get(channelId)
    if (!ch)
      return

    ch.state = 'closed'
    this.session.abortRekey(ch)
    const termination = opts?.streamTermination ?? 'end'
    const reason = opts?.reason ?? new ChannelError('client', 'channel closed')
    this.rpc.drainChannel(ch, reason, termination)

    this.pool.delete(channelId)
    this.stopIdleRekeyTimerIfEmpty()

    this.notifyStateChange()

    // Tell the Hub to clean up.
    try {
      await this.transport.closeChannel(channelId)
    }
    catch {
      // Best effort.
    }
  }

  /**
   * Send a unary RPC request through the encrypted channel.
   *
   * The optional `signal` lets the caller short-circuit the wait
   * locally: when it fires, the pendingRequest entry is dropped and
   * the returned promise rejects with `signal.reason`. The encrypted
   * channel has no per-call cancellation message today, so any
   * in-flight worker work continues until it completes — but the
   * caller no longer holds the pending entry (it'd be dropped on
   * receipt anyway). Worth threading even without a worker-side
   * cancel: future channel revisions can add one without changing
   * any caller.
   */
  call(channelId: string, method: string, payload: Uint8Array, timeoutMs?: number, signal?: AbortSignal): Promise<InnerRpcResponse> {
    const ch = this.pool.get(channelId)
    if (!ch || ch.state === 'closed') {
      return Promise.reject(new ChannelError('client', 'channel not open'))
    }
    if (signal?.aborted) {
      return Promise.reject(abortError(signal, method))
    }

    // Gate every send on the hard ceiling (matches Go ensureRekeyed): a
    // reject backoff makes shouldInitiateRekey false even when age is past 70m.
    if (this.session.needsRekeyGate(ch)) {
      return this.session.ensureRekeyed(ch).then(() => {
        if (ch.state === 'closed' || !this.pool.has(channelId)) {
          return Promise.reject(new ChannelError('client', 'channel not open'))
        }
        if (signal?.aborted) {
          return Promise.reject(abortError(signal, method))
        }
        return this.rpc.callAfterRekey(ch, channelId, method, payload, timeoutMs, signal)
      })
    }

    // Fast path: no rekey needed — register and send synchronously so callers
    // (and tests) that reply in the same turn still find the pending handler.
    return this.rpc.callAfterRekey(ch, channelId, method, payload, timeoutMs, signal)
  }

  /**
   * Send a streaming RPC request through the encrypted channel.
   * Returns a handle for receiving stream messages.
   */
  stream(channelId: string, method: string, payload: Uint8Array): {
    requestId: number
    onMessage: (cb: (msg: InnerStreamMessage) => void) => void
    onEnd: (cb: () => void) => void
    onError: (cb: (err: Error) => void) => void
    send: (payload: Uint8Array) => void
    cancel: () => void
  } {
    const ch = this.pool.get(channelId)
    // Streams require a verified session; call() still allows 'opening' so the
    // open-time Ping can complete before the channel is published as ready.
    if (!ch || ch.state !== 'verified') {
      throw new ChannelError('client', 'channel not open')
    }

    const handle = this.rpc.beginStream(ch, method, payload)

    // Defer listener registration until after rekey when a rekey/ceiling path
    // runs: ensureRekeyed may closeChannel→drainChannel with onEnd, and a
    // request that never hit the wire must terminal only via onError.
    if (this.session.needsRekeyGate(ch)) {
      void this.session.ensureRekeyed(ch).then(() => {
        if (ch.state === 'closed' || !this.pool.has(channelId)) {
          queueMicrotask(() => {
            handle.deliverDeferredError(new ChannelError('client', 'channel not open'), 'stream send failed with no onError listener')
          })
          return
        }
        const sendErr = handle.attachAndSend()
        if (sendErr) {
          queueMicrotask(() => {
            handle.deliverDeferredError(sendErr, 'stream send failed with no onError listener')
          })
        }
      }).catch((err) => {
        const e = err instanceof Error ? err : new ChannelError('client', formatErrorMessage(err))
        queueMicrotask(() => {
          handle.deliverDeferredError(e, 'stream rekey failed with no onError listener')
        })
      })
    }
    else {
      // Fast path: register and encrypt+send synchronously so the request is
      // on the wire before the handle returns (existing callers and tests rely
      // on that).
      const sendErr = handle.attachAndSend()
      if (sendErr)
        throw sendErr
    }

    const sendOnStream = (payload: Uint8Array, cancel: boolean): void => {
      const live = this.pool.get(channelId)
      if (!live || live.state === 'closed') {
        if (cancel)
          return
        throw new ChannelError('client', 'channel not open')
      }
      const doSend = (): ChannelError | null => this.rpc.sendOnStream(live, handle.requestId, payload, cancel)
      if (this.session.needsRekeyGate(live)) {
        // Rekey must complete before the frame is meaningful; await it so
        // callers learn about send failure instead of advancing local state
        // on a fire-and-forget drop. deliverDeferredError fires the handle's
        // onError listener when registered (the watch stream always registers
        // one), so a post-rekey channel death clears inflight interest and
        // triggers a reconnect rather than leaving local state believing the
        // revision landed.
        void this.session.ensureRekeyed(live).then(() => {
          if (live.state === 'closed' || !this.pool.has(channelId)) {
            if (!cancel) {
              queueMicrotask(() => {
                handle.deliverDeferredError(
                  new ChannelError('client', 'channel not open'),
                  'stream update dropped after rekey with no onError listener',
                )
              })
            }
            return
          }
          const err = doSend()
          if (err && !cancel) {
            queueMicrotask(() => {
              handle.deliverDeferredError(err, 'stream update failed with no onError listener')
            })
          }
        }).catch((err) => {
          if (cancel)
            return
          const e = err instanceof Error ? err : new ChannelError('client', formatErrorMessage(err))
          queueMicrotask(() => {
            handle.deliverDeferredError(e, 'stream update rekey failed with no onError listener')
          })
        })
        return
      }
      const err = doSend()
      if (err && !cancel)
        throw err
    }

    return {
      requestId: handle.requestId,
      onMessage: handle.onMessage,
      onEnd: handle.onEnd,
      onError: handle.onError,
      send: (payload: Uint8Array) => sendOnStream(payload, false),
      cancel: () => sendOnStream(new Uint8Array(), true),
    }
  }

  /** Get an open channel for a worker, or open a new one. */
  async getOrOpenChannel(workerId: string): Promise<string> {
    return this.pool.getOrOpenChannel(workerId, {
      openChannel: id => this.openChannelUncached(id),
      closeChannel: id => this.closeChannel(id),
      pastHardCeiling: ch => this.session.pastHardCeiling(ch),
      shouldInitiateRekey: ch => this.session.shouldInitiateRekey(ch),
      ensureRekeyed: ch => this.session.ensureRekeyed(ch),
      expectedUserId: () => this.expectedUserIdFn(),
    })
  }

  /** Check if a channel is open. */
  isOpen(channelId: string): boolean {
    return this.pool.isOpen(channelId)
  }

  /**
   * Whether a usable channel to this worker already exists.
   *
   * For callers that only have something to say IF a channel is already
   * up, and for whom opening one would be self-defeating — retiring
   * subscriptions is the case: a channel that does not exist holds no
   * subscriptions, so opening one (a full Noise_NK + ML-KEM handshake
   * plus a hub round trip) purely to announce that nothing is wanted is
   * pure cost.
   *
   * Mirrors getOrOpenChannel's reuse test, `verified` included: an open
   * still in progress is not yet a channel anyone could have subscribed
   * on.
   */
  hasOpenChannelForWorker(workerId: string): boolean {
    return this.pool.hasOpenChannelForWorker(workerId)
  }

  /** Close all channels and the shared WebSocket. */
  closeAll(): void {
    // First, invalidate every in-flight open (see closeGeneration): the
    // snapshot below cannot see a channel that has not registered yet.
    this.pool.bumpCloseGeneration()
    // Snapshot the ids before iterating: closeChannel deletes from the pool,
    // and a listener it notifies could in principle open a channel mid-loop. ES
    // makes deleting the current entry safe, but a newly-added entry would be
    // visited by a live iterator; iterating a snapshot makes the intent explicit.
    for (const channelId of [...this.pool.keys()])
      void this.closeChannel(channelId)
    this.relay.closeWebSocket()
  }

  /**
   * High-level typed RPC call through the encrypted channel.
   * Opens a channel to the worker if needed.
   */
  async callWorker<
    ReqSchema extends GenMessage<any>,
    RespSchema extends GenMessage<any>,
  >(
    workerId: string,
    method: string,
    reqSchema: ReqSchema,
    respSchema: RespSchema,
    req: MessageInitShape<ReqSchema>,
    opts?: { timeoutMs?: number, signal?: AbortSignal },
  ): Promise<MessageShape<RespSchema>> {
    if (opts?.signal?.aborted) {
      throw abortError(opts.signal, method)
    }
    const channelId = await this.getOrOpenChannel(workerId)
    // Re-check after the (potentially async) channel-open: a long
    // handshake gives the caller plenty of time to abort. Skipping
    // this check would still get caught by call()'s pre-check, but
    // checking here saves the protobuf encode round-trip below.
    if (opts?.signal?.aborted) {
      throw abortError(opts.signal, method)
    }
    const msg = create(reqSchema, req)
    if (log.isDebug())
      log.debug('callWorker request', { method, request: toJsonString(reqSchema, msg) })
    const payload = toBinary(reqSchema, msg)
    let resp
    try {
      resp = await this.call(channelId, method, payload, opts?.timeoutMs, opts?.signal)
    }
    catch (err) {
      log.debug('callWorker error', { method, error: formatErrorMessage(err) })
      throw err
    }
    const result = fromBinary(respSchema, resp.payload)
    if (log.isDebug())
      log.debug('callWorker response', { method, response: toJsonString(respSchema, result) })
    return result
  }

  /**
   * Remove a stream listener for a specific request on a channel.
   * Called when the client aborts a stream to prevent the old listener
   * from processing events after a stream restart.
   */
  removeStreamListener(channelId: string, requestId: number): void {
    const ch = this.pool.get(channelId)
    if (ch) {
      this.rpc.unregisterRequest(ch, requestId)
    }
  }

  // ---- Private methods ----

  /**
   * Decide a channel's fate after sendEncryptedMessage threw.
   *
   * Only a session-level failure kills the channel. `encrypt` throwing means the Noise
   * send state is finished (the nonce ceiling), and a chunked send that threw midway
   * has already put chunks on the wire, leaving the peer's receive nonce ahead of ours
   * -- either way every later send on this channel is garbage. Cancelling it is what
   * lets pooled callers re-resolve onto a fresh one: getOrOpenChannel caches by worker
   * and nothing else evicts a channel before its SESSION_KEY_HARD_CEILING_MS check, so a
   * poisoned session left in the pool would be handed to every later caller and fail
   * identically until the hard ceiling. The Go client cancels the same way.
   *
   * A `client` ChannelError -- today, a payload over maxReassembledMessageSize -- is the opposite
   * case: the session never encrypted a byte and is untouched, so tearing the channel
   * down would punish every other caller for one bad call.
   */
  private onSendFailure(ch: ActiveChannel, err: unknown): void {
    if (err instanceof ChannelError && err.source === 'client')
      return
    log.error('encrypting a channel message failed, closing the channel', { channel_id: ch.channelId, error: err })
    ch.state = 'closed'
    const reason = err instanceof ChannelError
      ? err
      : new ChannelError('transport', formatErrorMessage(err))
    // Defer close so trySend does not re-enter the RPC mux on the same stack.
    // Failure teardowns must terminate streams with error, not orderly end.
    queueMicrotask(() => {
      void this.closeChannel(ch.channelId, { streamTermination: 'error', reason })
    })
  }

  private handleHubControl(msg: ChannelMessage): void {
    try {
      const frame = fromBinary(HubControlFrameSchema, msg.ciphertext)
      for (const cb of this.hubControlListeners) {
        try {
          cb(frame)
        }
        catch (err) {
          log.error('hub control listener error', { error: err })
        }
      }
    }
    catch (err) {
      log.error('failed to parse hub control frame', { error: err })
    }
  }

  private handleRelayCloseDrain(successorDialing: boolean, fatal?: FatalCloseInfo): void {
    // A terminal close carries a reason the user can act on ("close a tab"),
    // and this drain error is what reaches them: every caller above wraps it
    // with showWarnToast, whose fallback copy formatErrorMessage discards in
    // favour of err.message. Draining with a generic string here is what turned
    // a connection-cap refusal into "Failed to open terminal".
    const reason = fatal
      ? fatalCloseError(fatal)
      : new ChannelError('transport', 'channel disconnected')
    for (const channelId of [...this.pool.keys()]) {
      const ch = this.pool.get(channelId)
      if (!ch)
        continue
      this.rpc.drainChannel(ch, reason, 'error')
      ch.state = 'closed'
      this.pool.delete(channelId)
    }
    this.stopIdleRekeyTimerIfEmpty()

    if (!successorDialing) {
      // Same fence as closeAll: in-flight openChannel factories must not
      // register after the transport died (FOOTGUNS-2).
      this.pool.bumpCloseGeneration()
      this.pool.clearOpening()
    }
    this.notifyStateChange()
  }
}
