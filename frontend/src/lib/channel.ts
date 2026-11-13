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
 *   - channelPool.ts — channel map, open dedup, identity reuse
 *   - channelOpen.ts — pin → handshake → verify → commit open path
 *   - channelInbound.ts — CLOSE flag / forwarding to the session worker / InnerMessage dispatch
 *   - channelSessionClient.ts — the main thread's side of the channel session worker
 *
 * The Noise session of every channel runs in the channel session worker
 * (channelSessionWorker.ts): the handshake, encryption, decryption, chunking,
 * reassembly and the in-band rekey (channelSession.ts, channelSessionInbound.ts,
 * channelSessionEndpoint.ts). This thread never holds a cipher key.
 *
 * See https://github.com/leapmux/leapmux/issues/292.
 */
import type { DescMessage, DescMethodServerStreaming, DescMethodUnary, MessageInitShape, MessageShape } from '@bufbuild/protobuf'
import type { OpeningChannel } from './channelOpen'
import type { ChannelSocket } from './channelRelay'
import type { StreamAction } from './channelRpc'
import type { SessionWorkerPort } from './channelSessionClient'
import type { WorkerKeyBundle } from './workerKeyBundle'
import type { FatalCloseInfo } from './wsCloseCodes'
import type { EncryptionMode } from '~/generated/proto/leapmux/v1/channel_pb'
import { create, fromBinary, toBinary, toJsonString } from '@bufbuild/protobuf'
import { WorkerSystemService } from '~/generated/proto/leapmux/v1/worker_system_pb'
import { abortError, ChannelError, channelNotOpenError } from './channelError'
import { ChannelInbound } from './channelInbound'
import { ChannelOpen } from './channelOpen'
import { ChannelPool } from './channelPool'
import { ChannelRelay } from './channelRelay'
import { ChannelRpcMux } from './channelRpc'
import { ChannelSessionClient } from './channelSessionClient'
import { formatErrorMessage } from './errors'
import { fatalCloseError } from './fatalCloseMessage'
import { KeyPinStore } from './keyPinStore'
import { createLogger } from './logger'
import { procedureOf } from './rpcProcedure'

export type { ChannelErrorSource } from './channelError'
export { abortError, ChannelError } from './channelError'
export type { ChannelSocket } from './channelRelay'
export type { KeyPinDecision } from './keyPinStore'
export { clearAllKeyPins, clearKeyPin, KeyPinRejectedError, KeyPinStore } from './keyPinStore'
export type { WorkerKeyBundle } from './workerKeyBundle'

const log = createLogger('channel')

// safeCall invokes a user-supplied listener callback and swallows any throw so
// one throwing listener cannot break the iteration that notifies the rest. The
// state, error, and stream-teardown loops all fan out to consumer callbacks; an
// uncaught throw in one would leave later listeners unnotified and teardown
// half-done (a leaked Hub-side channel, a stranded pending request).
function safeCall(fn: () => void, description: string): void {
  try {
    fn()
  }
  catch (err) {
    log.warn('listener threw; continuing with the remaining listeners', { what: description, error: err })
  }
}

/**
 * One server-streaming call on an open channel.
 *
 * The method descriptor types both directions: the opening request and `send`
 * take the method's input message, and `onMessage` receives its output message.
 */
export interface ChannelStream<I extends DescMessage, O extends DescMessage> {
  requestId: number
  /**
   * Receives each frame, decoded with the method's output schema. A frame that
   * does not decode never reaches this listener: it ends the stream through
   * `onError`, and the stream's owner reconnects.
   */
  onMessage: (cb: (msg: MessageShape<O>) => void) => void
  onEnd: (cb: () => void) => void
  onError: (cb: (err: Error) => void) => void
  /** Revise the open stream. Encodes `req` with the method's input schema. */
  send: (req: MessageInitShape<I>) => void
  /** Cancel the stream on the wire and drop the local listener. */
  cancel: () => void
}

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

/** A pooled channel, from 'opening' through 'closed'. */
type ActiveChannel = OpeningChannel

/**
 * Fallback RPC call timeout in milliseconds, used only when the owner
 * doesn't inject a `rpcTimeoutFn`. Must be larger than the worker's own
 * `apiTimeoutSeconds` context deadline (10s default) so the worker has time
 * to respond with DeadlineExceeded before this client-side timer fires.
 * 15_000 == 10s × the 1.5× multiplier applied by ~/api/transport.
 */
const FALLBACK_RPC_TIMEOUT_MS = 15_000

/** The channel session worker factory, and optional overrides for testing (dependency injection). */
export interface ChannelManagerOpts {
  /**
   * Spawn the channel session worker, which holds the Noise session of every
   * channel. A browser passes spawnChannelSessionWorker; Node and the unit tests
   * pass the spawn of an in-process host (channelSessionInProcess.ts). The
   * manager calls it lazily, at the first open, and again after the worker is
   * lost or shut down.
   */
  spawnSessionWorker: () => SessionWorkerPort
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
   * Install document visibility / pageshow wake listeners that force a rekey /
   * hard-ceiling check after OS suspend. Default true when `document` exists.
   * Tests that drive wake via checkChannelsAfterWake pass false. The idle rekey
   * timer runs in the session worker (ChannelSessionEndpointOptions).
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
  private sessionClient: ChannelSessionClient
  private rpc: ChannelRpcMux
  private open: ChannelOpen<ActiveChannel>
  private inbound: ChannelInbound<ActiveChannel>
  private testPayloadBudget: number | undefined
  private testReassembledCeiling: number | undefined
  private rpcTimeoutFn: () => number
  private expectedUserIdFn: () => string | undefined
  private keyPins: KeyPinStore

  private stateListeners = new Set<() => void>()
  private errorListeners = new Set<(workerId: string, error: ChannelError) => void>()
  private fatalCloseListeners = new Set<(info: FatalCloseInfo) => void>()

  /**
   * Test seam: channel.*.test.ts reaches through `(mgr as any).channels`. The live
   * map lives on ChannelPool; this getter keeps that access path working.
   */
  private get channels(): Map<string, ActiveChannel> {
    return this.pool.asMap()
  }

  constructor(transport: ChannelTransport, opts: ChannelManagerOpts) {
    this.transport = transport
    this.testPayloadBudget = opts.testPayloadBudget
    this.testReassembledCeiling = opts.testReassembledCeiling
    this.rpcTimeoutFn = opts.rpcTimeoutFn ?? (() => FALLBACK_RPC_TIMEOUT_MS)
    this.expectedUserIdFn = opts.expectedUserId ?? (() => undefined)
    this.keyPins = opts.keyPins ?? new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })

    this.relay = new ChannelRelay({
      createWebSocket: () => this.transport.createWebSocket(),
      wsOpenTimeoutMs: opts.wsOpenTimeoutMs ?? 10_000,
      onFrame: (channelId, msg) => this.inbound.handleMessage(channelId, msg),
      onCloseDrain: (successorDialing, fatal) => this.handleRelayCloseDrain(successorDialing, fatal),
      onFatalClose: info => this.notifyFatalClose(info),
    })

    this.sessionClient = new ChannelSessionClient({
      spawn: opts.spawnSessionWorker,
      onWire: (channelId, frame) => this.sendFrame(channelId, frame),
      onDeliver: (channelId, correlationId, plaintext) => this.inbound.deliver(channelId, correlationId, plaintext),
      onReassemblyFailed: (channelId, correlationId, message) => this.inbound.reassemblyFailed(channelId, correlationId, message),
      onSessionEnded: (channelId, reason) => {
        // The channel may be closed already: closeChannel then does nothing.
        void this.closeChannel(channelId, { streamTermination: 'error', reason })
      },
      onWorkerLost: err => this.handleWorkerLost(err),
    })

    this.rpc = new ChannelRpcMux({
      send: (ch, plaintext, requestId, startsRequest) => this.sendPlaintext(ch as ActiveChannel, plaintext, requestId, startsRequest),
      release: (ch, correlationId) => this.sessionClient.release(ch.channelId, correlationId),
      onSendFailure: (ch, err) => this.onSendFailure(ch as ActiveChannel, err),
      rpcTimeoutFn: () => this.rpcTimeoutFn(),
      notifyError: (workerId, error) => this.notifyError(workerId, error),
      safeCall,
    })

    this.open = new ChannelOpen({
      transport: this.transport,
      keyPins: this.keyPins,
      session: this.sessionClient,
      relay: this.relay,
      pool: this.pool,
      expectedUserId: () => this.expectedUserIdFn(),
      ...(this.testPayloadBudget !== undefined
        ? { testPayloadBudget: this.testPayloadBudget }
        : {}),
      ...(this.testReassembledCeiling !== undefined
        ? { testReassembledCeiling: this.testReassembledCeiling }
        : {}),
      verifySession: ch => this.verifySession(ch),
      evictGhost: (channelId, reason) => this.evictGhost(channelId, reason),
      notifyStateChange: () => this.notifyStateChange(),
    })

    this.inbound = new ChannelInbound({
      getChannel: id => this.pool.get(id),
      rpc: this.rpc,
      session: this.sessionClient,
      closedByPeer: id => this.closedByPeer(id),
    })

    const installWake = opts.installWakeListener ?? (typeof document !== 'undefined')
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

  /**
   * The listeners stay for the life of the page: the browser's manager is a
   * singleton (~/api/workerRpc), so nothing ever removes them.
   */
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
  }

  /**
   * Rekey every session whose rekey is due, and end each one past its hard
   * ceiling, after the OS suspended the page. The session worker owns the policy
   * and its clock.
   */
  checkChannelsAfterWake(): void {
    this.sessionClient.checkRekey()
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
   *
   * Ping is WorkerSystemService.Ping, a no-op that proves the session decrypts in
   * both directions. Its procedure comes from the generated descriptor, as the
   * worker's registration takes it from the generated Go constant.
   */
  private async verifySession(channel: ActiveChannel): Promise<void> {
    try {
      await this.call(channel.channelId, procedureOf(WorkerSystemService.method.ping), new Uint8Array())
    }
    catch (err) {
      const failure = err instanceof ChannelError
        ? err
        : new ChannelError('transport', `verify channel session: ${formatErrorMessage(err)}`)
      this.evictGhost(channel.channelId, failure)
      throw failure
    }
    channel.state = 'verified'
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
    this.retire(ghost, err, 'error')
  }

  /**
   * The one teardown of a pooled channel, which every close path runs. The
   * channel is closed and out of the pool before any listener runs, so a listener
   * that re-enters the manager (a stream owner that reconnects at once) can never
   * find it, and the session worker zeroes its keys.
   */
  private retire(ch: ActiveChannel, reason: ChannelError, termination: 'end' | 'error'): void {
    ch.state = 'closed'
    this.pool.delete(ch.channelId)
    this.sessionClient.close(ch.channelId)
    this.rpc.drainChannel(ch, reason, termination)
  }

  /** Close an encrypted channel (does not close the shared WebSocket). */
  async closeChannel(
    channelId: string,
    opts?: { streamTermination?: 'end' | 'error', reason?: ChannelError },
  ): Promise<void> {
    const ch = this.pool.get(channelId)
    if (!ch)
      return

    this.retire(
      ch,
      opts?.reason ?? new ChannelError('client', 'channel closed', { disconnected: true }),
      opts?.streamTermination ?? 'end',
    )

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
   * Send a unary RPC request through the encrypted channel. The promise
   * resolves with the serialized response message, and rejects with the
   * worker's in-band error.
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
  call(channelId: string, method: string, payload: Uint8Array, timeoutMs?: number, signal?: AbortSignal): Promise<Uint8Array> {
    const ch = this.pool.get(channelId)
    if (!ch || ch.state === 'closed') {
      return Promise.reject(channelNotOpenError())
    }
    if (signal?.aborted) {
      return Promise.reject(abortError(signal, method))
    }

    // Register and send synchronously. A send while a rekey is due waits in the
    // session worker until the rekey settles (see ChannelSession.send), and an
    // abort while it waits releases the id, so the request never reaches the wire.
    return this.rpc.call(ch, method, payload, timeoutMs, signal)
  }

  /**
   * Send a streaming RPC request through the encrypted channel.
   * Returns a handle for receiving stream messages.
   *
   * The method descriptor supplies the procedure path and both message schemas,
   * so a caller cannot pair a method with the wrong request or message type.
   */
  stream<I extends DescMessage, O extends DescMessage>(channelId: string, method: DescMethodServerStreaming<I, O>, req: MessageInitShape<I>): ChannelStream<I, O> {
    const ch = this.pool.get(channelId)
    // Streams require a verified session; call() still allows 'opening' so the
    // open-time Ping can complete before the channel is published as ready.
    if (!ch || ch.state !== 'verified') {
      throw channelNotOpenError()
    }

    const procedure = procedureOf(method)
    const encode = (msg: MessageInitShape<I>): Uint8Array => toBinary(method.input, create(method.input, msg))
    // Registers the listener and sends the opening request. A send while a
    // rekey is due waits in the session worker until the rekey settles, and a
    // failure that the rekey or the wire meets later closes the channel, which
    // ends the stream through onError.
    const handle = this.rpc.beginStream(ch, procedure, encode(req))

    const sendOnStream = (action: StreamAction): void => {
      const cancel = action.case === 'cancel'
      const live = this.pool.get(channelId)
      if (!live || live.state === 'closed') {
        if (cancel)
          return
        throw channelNotOpenError()
      }
      const err = this.rpc.sendOnStream(live, handle.requestId, action)
      if (err && !cancel)
        throw err
    }

    const deliverDecoded = (payload: Uint8Array, deliver: (msg: MessageShape<O>) => void): void => {
      let msg: MessageShape<O>
      try {
        msg = fromBinary(method.output, payload)
      }
      catch (err) {
        // Every stream takes this one policy: a skipped frame could hide a
        // lost event, and a reconnect replays the state. Cancel first, so the
        // worker stops the stream although the owner's listener may throw.
        sendOnStream({ case: 'cancel' })
        handle.fail(
          new ChannelError('client', `decode a message of stream ${procedure}: ${formatErrorMessage(err)}`),
          'stream decode failed with no onError listener',
        )
        return
      }
      if (log.isDebug()) {
        log.debug('stream message', {
          method: procedure,
          message: toJsonString(method.output, msg),
        })
      }
      deliver(msg)
    }

    return {
      requestId: handle.requestId,
      onMessage: cb => handle.onMessage(frame => deliverDecoded(frame.payload, cb)),
      onEnd: handle.onEnd,
      onError: handle.onError,
      send: req => sendOnStream({
        case: 'payload',
        payload: encode(req),
      }),
      cancel: () => sendOnStream({ case: 'cancel' }),
    }
  }

  /** Get an open channel for a worker, or open a new one. */
  async getOrOpenChannel(workerId: string): Promise<string> {
    return this.pool.getOrOpenChannel(workerId, {
      openChannel: id => this.openChannelUncached(id),
      closeChannel: id => this.closeChannel(id),
      ensureReady: ch => this.sessionClient.ensureReady(ch.channelId),
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

  /**
   * Close all channels, the shared WebSocket, and the channel session worker,
   * which zeroes every key. The next open starts a fresh worker.
   */
  closeAll(): void {
    // First, invalidate every in-flight open (see closeGeneration): the
    // snapshot below cannot see a channel that has not registered yet.
    this.pool.bumpCloseGeneration()
    // Snapshot the ids before iterating: closeChannel deletes from the pool,
    // and a listener it notifies could in principle open a channel mid-loop. ES
    // makes deleting the current entry safe, but a newly-added entry would be
    // visited by a live iterator; iterating a snapshot makes the intent explicit.
    for (const channelId of [
      ...this.pool.keys(),
    ])
      void this.closeChannel(channelId)
    this.relay.closeWebSocket()
    this.sessionClient.shutdown()
  }

  /**
   * High-level typed RPC call through the encrypted channel.
   * Opens a channel to the worker if needed.
   *
   * The method descriptor supplies the procedure path and both message schemas,
   * so a caller cannot pair a method with the wrong request or response type.
   */
  async callWorker<I extends DescMessage, O extends DescMessage>(
    workerId: string,
    method: DescMethodUnary<I, O>,
    req: MessageInitShape<I>,
    opts?: { timeoutMs?: number, signal?: AbortSignal },
  ): Promise<MessageShape<O>> {
    const procedure = procedureOf(method)
    const reqSchema = method.input
    const respSchema = method.output
    if (opts?.signal?.aborted) {
      throw abortError(opts.signal, procedure)
    }
    const channelId = await this.getOrOpenChannel(workerId)
    // Re-check after the (potentially async) channel-open: a long
    // handshake gives the caller plenty of time to abort. Skipping
    // this check would still get caught by call()'s pre-check, but
    // checking here saves the protobuf encode round-trip below.
    if (opts?.signal?.aborted) {
      throw abortError(opts.signal, procedure)
    }
    const msg = create(reqSchema, req)
    if (log.isDebug()) {
      log.debug('callWorker request', {
        method: procedure,
        request: toJsonString(reqSchema, msg),
      })
    }
    const payload = toBinary(reqSchema, msg)
    let responsePayload
    try {
      responsePayload = await this.call(channelId, procedure, payload, opts?.timeoutMs, opts?.signal)
    }
    catch (err) {
      log.debug('callWorker error', {
        method: procedure,
        error: formatErrorMessage(err),
      })
      throw err
    }
    const result = fromBinary(respSchema, responsePayload)
    if (log.isDebug()) {
      log.debug('callWorker response', {
        method: procedure,
        response: toJsonString(respSchema, result),
      })
    }
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
   * Hand one outbound message to the session worker. A message over the
   * channel's ceiling throws to the caller and posts nothing; the one source of
   * that ceiling is resolveMessageLimits, which the open ran.
   */
  private sendPlaintext(ch: ActiveChannel, plaintext: Uint8Array, requestId: number, startsRequest: boolean): void {
    if (plaintext.length > ch.maxReassembledMessageSize) {
      throw new ChannelError('client', `message too large: ${plaintext.length} > ${ch.maxReassembledMessageSize}`)
    }
    this.sessionClient.send(ch.channelId, requestId, plaintext, startsRequest)
  }

  /**
   * Put one frame that the session worker encrypted on the WebSocket. The frames
   * arrive in nonce order, and leave in the same order. The frames of a closed
   * channel are dropped: its requests already failed.
   */
  private sendFrame(channelId: string, frame: Uint8Array): void {
    const ch = this.pool.get(channelId)
    if (!ch || ch.state === 'closed')
      return
    try {
      this.relay.send(frame)
    }
    catch (err) {
      this.onSendFailure(ch, err)
    }
  }

  /**
   * The session worker crashed or got stuck, and took the keys of every channel
   * with it. Close every channel with `err`, and fence the opens in flight, whose
   * handshakes lived in that worker. The next open starts a fresh worker.
   */
  private handleWorkerLost(err: ChannelError): void {
    this.pool.bumpCloseGeneration()
    for (const channelId of [
      ...this.pool.keys(),
    ])
      void this.closeChannel(channelId, { streamTermination: 'error', reason: err })
  }

  /** The peer sent the CLOSE flag for `channelId`. */
  private closedByPeer(channelId: string): void {
    const ch = this.pool.get(channelId)
    if (!ch)
      return
    this.retire(ch, new ChannelError('transport', 'channel closed by server'), 'error')
    this.notifyStateChange()
  }

  /**
   * Decide a channel's fate after a send failed. It runs for a failure that
   * sendPlaintext threw to the RPC mux, and for a frame that the WebSocket
   * refused (sendFrame). The session worker ends a session whose encrypt failed
   * on its own (a `sessionEnded` event).
   *
   * Only a transport or session failure kills the channel. A frame that the
   * WebSocket refused may be one of several frames of a message, which leaves the
   * peer's receive nonce out of step with ours -- every later send on this channel
   * is garbage. Cancelling it is what lets pooled callers re-resolve onto a fresh
   * one: getOrOpenChannel caches by worker, so a poisoned session left in the pool
   * would be handed to every later caller and fail identically. The Go client
   * cancels the same way.
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

  private handleRelayCloseDrain(successorDialing: boolean, fatal?: FatalCloseInfo): void {
    // A terminal close carries a reason the user can act on ("close a tab"),
    // and this drain error is what reaches them: every caller above wraps it
    // with showWarnToast, whose fallback copy formatErrorMessage discards in
    // favour of err.message. Draining with a generic string here is what turned
    // a connection-cap refusal into "Failed to open terminal".
    const reason = fatal
      ? fatalCloseError(fatal)
      : new ChannelError('transport', 'channel disconnected')
    for (const channelId of [
      ...this.pool.keys(),
    ]) {
      const ch = this.pool.get(channelId)
      if (!ch)
        continue
      this.retire(ch, reason, 'error')
    }

    if (!successorDialing) {
      // Same fence as closeAll: in-flight openChannel factories must not
      // register after the transport died (FOOTGUNS-2).
      this.pool.bumpCloseGeneration()
      this.pool.clearOpening()
    }
    this.notifyStateChange()
  }
}
