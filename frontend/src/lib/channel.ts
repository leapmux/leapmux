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
 */
import type { MessageInitShape, MessageShape } from '@bufbuild/protobuf'
import type { GenMessage } from '@bufbuild/protobuf/codegenv2'
import type { Session } from './noise'
import type { ChannelMessage, HubControlFrame, InnerRpcResponse, InnerStreamMessage } from '~/generated/leapmux/v1/channel_pb'
import { create, fromBinary, toBinary, toJsonString } from '@bufbuild/protobuf'
import { bytesToHex } from '@noble/hashes/utils.js'
import {
  ChannelMessageFlags,
  ChannelMessageSchema,
  EncryptionMode,
  HubControlFrameSchema,
  InnerMessageSchema,
  InnerRpcRequestSchema,
} from '~/generated/leapmux/v1/channel_pb'
import { KEY_KEY_PINS, localStorageGet, localStorageRemove, localStorageSet } from './browserStorage'
import { formatErrorMessage } from './errors'
import { createInflightCache } from './inflightCache'
import { createLogger } from './logger'
import { initiatorHandshake1 as classicHandshake1, initiatorHandshake2 as classicHandshake2, concatBytes } from './noise'
import { initiatorHandshake1, initiatorHandshake2 } from './noise-hybrid'
import { DEFAULT_MAX_MESSAGE_SIZE, MAX_CHUNK_SIZE, Reassembler } from './reassembler'

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

/** Reserved channel ID for Hub-originated control frames. */
const HUB_CONTROL_CHANNEL_ID = '_hub'

/**
 * The largest wire correlation id this client will route. See handleMessage: ids are
 * plain numbers here, so anything past the exact-integer range is dropped rather than
 * rounded onto another request's handler.
 */
const MAX_SAFE_CORRELATION_ID = BigInt(Number.MAX_SAFE_INTEGER)

/**
 * The no-op inner RPC openChannel round-trips to prove the E2EE session decrypts
 * in both directions before returning the channel. Must match the worker's
 * registered handler — the Go side keeps this name in `channelwire.PingMethod`,
 * and both sides pin it to the cross-language fixture
 * (testdata/channelwire_limits.json) so a rename on one reddens CI here instead
 * of desyncing the open-time Ping the other end expects.
 */
export const PING_METHOD = 'Ping'

export type KeyPinDecision = 'accept' | 'reject'

/**
 * Structured error for channel operations.
 * - `transport`: WebSocket disconnect/timeout, channel closed by server (connection-level)
 * - `stream`: Backend stream error (carries backend error code)
 * - `rpc`: Backend RPC error (carries backend error code)
 * - `client`: Client-side issues (channel not open, message too large)
 */
export type ChannelErrorSource = 'transport' | 'stream' | 'rpc' | 'client'

export class ChannelError extends Error {
  readonly source: ChannelErrorSource
  readonly code: number

  constructor(source: ChannelErrorSource, message: string, code = 0) {
    super(message)
    this.name = 'ChannelError'
    this.source = source
    this.code = code
  }
}

/** Worker key bundle returned by transport. */
export interface WorkerKeyBundle {
  x25519PublicKey: Uint8Array
  mlkemPublicKey: Uint8Array
  slhdsaPublicKey: Uint8Array
}

/**
 * The narrow slice of the WebSocket surface ChannelManager actually drives. Both a
 * native browser WebSocket and the Tauri IPC relay wrapper (TauriRelayWebSocket)
 * satisfy this structurally, so the transport can hand either back without an
 * `as unknown as WebSocket` double-cast -- a cast that erases the type and would let
 * a future read of a member this wrapper doesn't forward (bufferedAmount, url,
 * protocol) compile and silently return undefined on the Tauri path.
 */
export interface ChannelSocket {
  readyState: number
  // `send` takes exactly what ChannelManager writes -- an ArrayBuffer-backed
  // Uint8Array (or ArrayBuffer). Spelling the buffer generic keeps a native
  // WebSocket, whose send wants a BufferSource, assignable to this shape.
  send: (data: Uint8Array<ArrayBuffer> | ArrayBuffer) => void
  close: (code?: number, reason?: string) => void
  // `(ev: any)` keeps the listener loose enough that a native WebSocket's overloaded,
  // WebSocketEventMap-typed add/removeEventListener satisfy this interface.
  addEventListener: (type: string, listener: (ev: any) => void, opts?: { once?: boolean }) => void
  removeEventListener: (type: string, listener: (ev: any) => void) => void
}

/** Transport interface for platform-specific RPC and WebSocket creation. */
export interface ChannelTransport {
  /**
   * Fetches the public key material and live encryption mode in one round
   * trip. Both are needed before every OpenChannel, so they travel together.
   */
  getWorkerHandshakeParams: (workerId: string) => Promise<{ keys: WorkerKeyBundle, encryptionMode: EncryptionMode }>
  openChannel: (workerId: string, handshakePayload: Uint8Array) => Promise<{ channelId: string, handshakePayload: Uint8Array, userId: string }>
  closeChannel: (channelId: string) => Promise<void>
  createWebSocket: () => ChannelSocket
  /** Called when a previously-pinned worker public key changes. */
  confirmKeyPin: (workerId: string, expectedFingerprint: string, actualFingerprint: string) => Promise<KeyPinDecision>
}

interface KeyPin { publicKeyHex: string, firstSeen: number }
type KeyPinMap = Record<string, KeyPin>

interface PendingRequest {
  resolve: (resp: InnerRpcResponse) => void
  reject: (err: Error) => void
}

interface StreamListener {
  onMessage: (msg: InnerStreamMessage) => void
  onEnd: () => void
  onError: (err: Error) => void
}

/** Maximum channel age before re-handshake. */
const CHANNEL_MAX_AGE_MS = 60 * 60 * 1000 // 1 hour

interface ActiveChannel {
  channelId: string
  workerId: string
  session: Session
  /**
   * The identity the Hub authenticated this channel's open as. Recorded because
   * channels are POOLED for up to CHANNEL_MAX_AGE_MS: the open-time cross-check
   * only proves who the page was when the channel was created, so getOrOpenChannel
   * re-compares this before handing a pooled channel out (see its identity check).
   */
  userId: string
  pendingRequests: Map<number, PendingRequest>
  streamListeners: Map<number, StreamListener>
  reassembly: Reassembler
  nextRequestId: number
  /**
   * Lifecycle state, gating pool handout -- a single field rather than separate
   * `verified`/`closed` booleans, whose fourth combination (verified AND closed)
   * no gate ever distinguished:
   *   - 'opening': present in `channels` so the open-time Ping's reply can route
   *     (handleMessage looks the channel up by id), but NOT open for business.
   *     hasOpenChannel and getOrOpenChannel skip it, so a racing caller waits on
   *     the open (openingChannels dedups it onto the same one) instead of being
   *     handed a session that may yet prove dead.
   *   - 'verified': the Ping round-tripped, so the channel may be served.
   *   - 'closed': torn down. Every path that sets this also deletes the channel
   *     from `channels`, so a channel is only ever observed 'closed' transiently
   *     by a caller mid-teardown.
   */
  state: 'opening' | 'verified' | 'closed'
  openedAt: number
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
export interface ChannelManagerOpts {
  handshake1?: typeof initiatorHandshake1
  handshake2?: typeof initiatorHandshake2
  classicHandshake1?: typeof classicHandshake1
  classicHandshake2?: typeof classicHandshake2
  maxMessageSize?: number
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
}

/**
 * Encode an InnerRpcRequest into its InnerMessage envelope plaintext — the one
 * wire-encoding step `call` and `stream` share, so the request framing can only
 * be defined in one place.
 */
function buildRequestPlaintext(method: string, payload: Uint8Array): Uint8Array {
  const innerReq = create(InnerRpcRequestSchema, {
    method,
    payload,
  })
  const envelope = create(InnerMessageSchema, {
    kind: { case: 'request', value: innerReq },
  })
  return toBinary(InnerMessageSchema, envelope)
}

/**
 * ChannelManager manages encrypted E2EE channels to Workers.
 *
 * It currently braids five responsibilities -- WebSocket transport lifecycle, Noise
 * crypto/handshake, channel pooling + identity re-check, RPC dispatch/streaming, and
 * reassembly delivery. The chunked-reassembly state machine was extracted to
 * `reassembler.ts`; decomposing the remaining transport / session / RPC-multiplexer
 * seams is a larger, entangled refactor tracked in
 * https://github.com/leapmux/leapmux/issues/292 (key-pinning extraction is #283).
 */
export class ChannelManager {
  private transport: ChannelTransport
  private channels = new Map<string, ActiveChannel>()
  /** In-flight openChannel promises per worker, for deduplication. */
  private openingChannels = createInflightCache<string, string>()
  private ws: ChannelSocket | null = null
  private wsPromise: Promise<void> | null = null
  private handshake1: typeof initiatorHandshake1
  private handshake2: typeof initiatorHandshake2
  private classicHS1: typeof classicHandshake1
  private classicHS2: typeof classicHandshake2
  private maxMessageSize: number
  private rpcTimeoutFn: () => number
  private expectedUserIdFn: () => string | undefined
  /** Workers whose keys were rejected by the user during this session. */
  private rejectedWorkers = new Set<string>()
  /**
   * Bumped by every closeAll. An in-flight openChannel captures it before its
   * first await and re-checks it before registering the channel: closeAll's
   * eager release snapshots this.channels, so an open still parked on an await
   * when the snapshot was taken would otherwise register AFTER it and survive
   * the release -- the identity-transition TOCTOU the eager release exists to
   * close (the lazy staleReason re-check still prevents cross-user REUSE, but
   * the leaked channel and its socket would linger).
   */
  private closeGeneration = 0

  // Observability hooks
  private stateListeners = new Set<() => void>()
  private errorListeners = new Set<(workerId: string, error: ChannelError) => void>()
  private hubControlListeners = new Set<(frame: HubControlFrame) => void>()

  constructor(transport: ChannelTransport, opts?: ChannelManagerOpts) {
    this.transport = transport
    this.handshake1 = opts?.handshake1 ?? initiatorHandshake1
    this.handshake2 = opts?.handshake2 ?? initiatorHandshake2
    this.classicHS1 = opts?.classicHandshake1 ?? classicHandshake1
    this.classicHS2 = opts?.classicHandshake2 ?? classicHandshake2
    this.maxMessageSize = opts?.maxMessageSize ?? DEFAULT_MAX_MESSAGE_SIZE
    this.rpcTimeoutFn = opts?.rpcTimeoutFn ?? (() => FALLBACK_RPC_TIMEOUT_MS)
    this.expectedUserIdFn = opts?.expectedUserId ?? (() => undefined)
  }

  /** Subscribe to channel state changes (open/close). Returns an unsubscribe function. */
  onStateChange(cb: () => void): () => void {
    this.stateListeners.add(cb)
    return () => {
      this.stateListeners.delete(cb)
    }
  }

  /** Subscribe to non-transport channel errors. Returns an unsubscribe function. */
  onChannelError(cb: (workerId: string, error: ChannelError) => void): () => void {
    this.errorListeners.add(cb)
    return () => {
      this.errorListeners.delete(cb)
    }
  }

  /** Subscribe to Hub control frames. Returns an unsubscribe function. */
  onHubControl(cb: (frame: HubControlFrame) => void): () => void {
    this.hubControlListeners.add(cb)
    return () => {
      this.hubControlListeners.delete(cb)
    }
  }

  /**
   * Check if a usable channel exists for a worker.
   *
   * An open still awaiting its verification Ping does not count: it is in `channels`
   * only so the ping's reply can route, and its session is not yet known to work.
   *
   * A channel whose Hub-authenticated identity has drifted from who this page now is
   * (a logout/login or impersonation switch left an up-to-an-hour-old channel
   * authenticated as another user) does NOT count either: getOrOpenChannel would
   * reject and reopen it on the next RPC, so reporting "connected" for it would show
   * a live link the current user cannot actually use as themselves. The aged /
   * needs-rekey reasons in staleReason are deliberately NOT applied here -- their
   * rotation is transparent to the caller, so a channel mid-rekey or a minute past
   * the age cap is still "connected" for indicator purposes.
   */
  hasOpenChannel(workerId: string): boolean {
    for (const ch of this.channels.values()) {
      if (ch.workerId === workerId && ch.state === 'verified'
        && !this.identityMismatch(this.expectedUserIdFn(), ch.userId)) {
        return true
      }
    }
    return false
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

  /**
   * Open an encrypted channel to a Worker.
   * Performs the Noise_NK handshake, key pinning check, connects the shared
   * WebSocket relay, and verifies the session with a Ping round trip.
   */
  async openChannel(workerId: string): Promise<string> {
    // Captured before the first await; see closeGeneration.
    const openedGeneration = this.closeGeneration
    // 1. Get Worker's handshake params (keys + live encryption mode) in one RPC.
    const { keys: keyBundle, encryptionMode: mode } = await this.transport.getWorkerHandshakeParams(workerId)

    // 2. Key pinning (TOFU model) — resolve the pin now, record it once the channel
    //    is proven (see commitPin below).
    const commitPin = await this.resolveKeyPin(workerId, keyBundle)

    // 3. Build handshake message 1 based on encryption mode. Completing the
    //    handshake (message 2) is deferred into the try below: it runs only after
    //    the Hub has registered the channel, so a malformed or forged handshake-2
    //    — wrong length, bad AEAD tag, invalid SLH-DSA signature — must roll that
    //    registration back like every later failure.
    let message1: Uint8Array
    let finishHandshake: (handshakePayload: Uint8Array) => Session

    if (mode === EncryptionMode.CLASSIC) {
      // Classical Noise_NK (X25519 only).
      const hs = this.classicHS1(keyBundle.x25519PublicKey)
      message1 = hs.message1
      finishHandshake = payload => this.classicHS2(hs.handshakeState, payload)
    }
    else {
      // Post-quantum hybrid Noise_NK (X25519 + ML-KEM + SLH-DSA).
      const hs = this.handshake1(keyBundle.x25519PublicKey, keyBundle.mlkemPublicKey)
      message1 = hs.message1
      finishHandshake = payload => this.handshake2(hs.handshakeState, payload, keyBundle.slhdsaPublicKey)
    }

    const result = await this.transport.openChannel(workerId, message1)

    // From here on the Hub has REGISTERED a channel and the Worker holds a live Noise
    // session, so every failure exit must tell the Hub to drop it. Without that, a
    // retry loop against a bad worker (a flaky relay failing the ping) strands a
    // channel per attempt -- on the Hub's index and as a Worker session plus its
    // goroutine and per-channel caps -- until the credential is revoked or the
    // process restarts. The Go client of this protocol rolls back at exactly this
    // boundary (backend/tunnel/channel.go's `rollback` flag +
    // rollbackRegisteredChannel).
    let registered = true
    const rollback = async () => {
      if (!registered)
        return
      registered = false
      try {
        await this.transport.closeChannel(result.channelId)
      }
      catch {
        // Best effort: the open is already failing and the Hub expires channels with
        // the credential that opened them, so a failed rollback must not mask the
        // real error.
      }
    }

    try {
      // Complete the Noise handshake. Verification of the Worker's handshake-2
      // message throws on tampering or corruption, and the Hub has already
      // registered the channel, so this sits inside the rollback's coverage — the
      // Go client covers the same step the same way (handshaker.finish runs under
      // the rollback defer in backend/tunnel/channel.go).
      const session = finishHandshake(result.handshakePayload)

      // Reject a hub that did not name the authenticated user, rather than falling
      // back to a locally-asserted identity: the whole point of binding to the
      // Hub-authenticated id is that a stale local one (an account or impersonation
      // switch) can never be asserted. The Go client of this protocol enforces the
      // same invariant at the same boundary (backend/tunnel/channel.go, "hub
      // returned an empty authenticated user id").
      if (!result.userId) {
        throw new ChannelError('transport', 'open channel: hub returned an empty authenticated user id')
      }

      // Cross-check the Hub's answer against who this page thinks it is. Taking the
      // identity and DISCARDING it — which is what this did before — means a tab
      // rendered as A whose cookie jar is now B opens a channel the Hub
      // authenticates as B and silently drives B's session with A's UI. The Hub
      // still wins; the open just fails instead of proceeding on a disagreement.
      const expectedUserId = this.expectedUserIdFn()
      if (this.identityMismatch(expectedUserId, result.userId)) {
        throw new ChannelError(
          'transport',
          `open channel: hub authenticated this channel as ${result.userId}, not the expected ${expectedUserId}`,
        )
      }

      // 4. Ensure shared WebSocket is connected.
      await this.ensureWebSocket()

      // A closeAll that ran while this open was parked on an await has already
      // snapshotted this.channels; registering now would slip past it (see
      // closeGeneration). Thrown inside the try so the catch below rolls the
      // Hub-registered channel back.
      if (this.closeGeneration !== openedGeneration) {
        throw new ChannelError('transport', 'open channel: superseded by a concurrent closeAll')
      }

      const channel: ActiveChannel = {
        channelId: result.channelId,
        workerId,
        session,
        userId: result.userId,
        pendingRequests: new Map(),
        streamListeners: new Map(),
        reassembly: new Reassembler(this.maxMessageSize),
        nextRequestId: 1,
        state: 'opening',
        openedAt: Date.now(),
      }

      // The channel must be in the map for the ping's reply to route (handleMessage
      // looks the channel up by id), but it goes in 'opening': the 'verified' state
      // is what hasOpenChannel and getOrOpenChannel gate on, so a caller racing this
      // open cannot be handed the channel before the ping proves it.
      this.channels.set(result.channelId, channel)

      // 5. Prove the session works end to end before handing the channel out.
      await this.verifySession(channel)

      // 6. Pin the key (TOFU on first use, or the key the user just accepted).
      //    Deliberately last: a key is only worth remembering once a channel to it
      //    has actually proven itself, and until here every exit rolls the open back.
      commitPin()
    }
    catch (err) {
      // Evict the channel if it already entered the pool. verifySession above
      // cleans up after itself, but a throw after the channel reached 'verified'
      // (say a future commitPin that can fail) would otherwise leave a verified ghost that
      // getOrOpenChannel serves for up to CHANNEL_MAX_AGE_MS while every RPC on it
      // times out against the Hub registration the rollback below drops. evictGhost
      // is a no-op when verifySession already removed the channel.
      this.evictGhost(
        result.channelId,
        err instanceof ChannelError ? err : new ChannelError('transport', `open channel: ${formatErrorMessage(err)}`),
      )
      await rollback()
      throw err
    }

    // The channel is the caller's now; closeChannel owns the Hub-side teardown.
    registered = false

    this.notifyStateChange()
    return result.channelId
  }

  /**
   * Prove the session works end to end before the channel is handed out, and
   * mark it verified.
   *
   * The Noise_NK handshake only proves THIS side can encrypt to the worker's
   * static key; it proves nothing about the worker's session decrypting, or its
   * replies decrypting back here. Without a round trip now, a session broken in
   * either direction opens "successfully" and fails on the caller's first real
   * call -- and channels are reused (getOrOpenChannel caches by worker), so the
   * broken one is served to every later caller until something evicts it. One
   * ping keeps the failure at the open, where it is attributable. On failure the
   * channel is evicted here so it never leaves this method verified.
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
  }

  /**
   * Remove a stranded channel from the pool and fail every request that slipped
   * onto it, once. It is the single teardown for a channel that entered
   * `channels` but must not be served: the ping-failure and post-verification
   * failure paths of openChannel both route through here so they cannot drift.
   *
   * Draining is not optional cleanup: once the channel is gone from `channels`,
   * neither closeChannel nor the WS teardown can reach a caller who slipped a
   * request onto it while the open was in flight -- left alone they would sit in
   * a map no one reads until their own RPC timeout fired ~15s later. A no-op when
   * the channel is not (or no longer) in the pool, so calling it twice is safe.
   */
  private evictGhost(channelId: string, err: ChannelError): void {
    const ghost = this.channels.get(channelId)
    if (!ghost)
      return
    ghost.state = 'closed'
    this.channels.delete(channelId)
    this.drainChannel(ghost, err, 'error')
  }

  /** Close an encrypted channel (does not close the shared WebSocket). */
  async closeChannel(channelId: string): Promise<void> {
    const ch = this.channels.get(channelId)
    if (!ch)
      return

    ch.state = 'closed'
    this.drainChannel(ch, new ChannelError('client', 'channel closed'), 'end')

    this.channels.delete(channelId)

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
    const ch = this.channels.get(channelId)
    if (!ch || ch.state === 'closed') {
      return Promise.reject(new ChannelError('client', 'channel not open'))
    }
    if (signal?.aborted) {
      return Promise.reject(signal.reason instanceof Error ? signal.reason : new ChannelError('client', `RPC call '${method}' aborted`))
    }

    const requestId = ch.nextRequestId++
    const effectiveTimeoutMs = timeoutMs ?? this.rpcTimeoutFn()

    return new Promise<InnerRpcResponse>((resolve, reject) => {
      const timeoutSec = Math.round(effectiveTimeoutMs / 1000)
      // timer + cleanup + abortListener form a mutually-referencing
      // teardown trio; lint-disable so the natural setup order can
      // stay together instead of being split by the no-use-before-define
      // rule.
      /* eslint-disable ts/no-use-before-define */
      let abortListener: (() => void) | undefined
      const cleanup = () => {
        clearTimeout(timer)
        if (abortListener && signal)
          signal.removeEventListener('abort', abortListener)
      }
      const timer = setTimeout(() => {
        this.unregisterRequest(ch, requestId)
        cleanup()
        log.debug('inner RPC request timed out', { channel_id: ch.channelId, id: requestId, method })
        reject(new ChannelError('client', `RPC call '${method}' timed out after ${timeoutSec}s (channel=${channelId})`))
      }, effectiveTimeoutMs)
      /* eslint-enable ts/no-use-before-define */

      log.debug('sending inner RPC request', { channel_id: ch.channelId, id: requestId, method, payload_len: payload.length })

      ch.pendingRequests.set(requestId, {
        resolve: (resp) => {
          cleanup()
          resolve(resp)
        },
        reject: (err) => {
          cleanup()
          reject(err)
        },
      })

      if (signal) {
        abortListener = () => {
          // Drop the pending entry so the eventual InnerRpcResponse
          // is treated as orphan + ignored (and with it any partial
          // reassembly, which existed only to feed this request).
          // cleanup() also clears the timer + this listener so no
          // double-resolve fires.
          this.unregisterRequest(ch, requestId)
          cleanup()
          log.debug('inner RPC request aborted by caller', { channel_id: ch.channelId, id: requestId, method })
          reject(signal.reason instanceof Error ? signal.reason : new ChannelError('client', `RPC call '${method}' aborted`))
        }
        signal.addEventListener('abort', abortListener, { once: true })
      }

      const plaintext = buildRequestPlaintext(method, payload)
      // The registration and the timer are already installed, and a throw out of a
      // Promise executor rejects the promise WITHOUT unwinding them -- the entry would
      // linger until the timeout fired and the timer would burn for its full duration
      // on a request that never reached the wire. Undo both here, then let
      // onSendFailure decide whether the channel itself is still usable.
      try {
        this.sendEncryptedMessage(ch, plaintext, requestId)
      }
      catch (err) {
        this.unregisterRequest(ch, requestId)
        cleanup()
        this.onSendFailure(ch, err)
        reject(err instanceof Error ? err : new ChannelError('client', formatErrorMessage(err)))
      }
    })
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
  } {
    const ch = this.channels.get(channelId)
    if (!ch || ch.state === 'closed') {
      throw new ChannelError('client', 'channel not open')
    }

    const requestId = ch.nextRequestId++
    let messageCb: ((msg: InnerStreamMessage) => void) | null = null
    let endCb: (() => void) | null = null
    let errorCb: ((err: Error) => void) | null = null

    log.debug('sending inner RPC request', { channel_id: ch.channelId, id: requestId, method, payload_len: payload.length })

    ch.streamListeners.set(requestId, {
      onMessage: msg => messageCb?.(msg),
      onEnd: () => endCb?.(),
      onError: err => errorCb?.(err),
    })

    const plaintext = buildRequestPlaintext(method, payload)
    // The listener is registered but the handle -- and with it the requestId the caller
    // would need to removeStreamListener -- has not been returned yet, so a throw here
    // leaves an entry NOBODY can ever reach or clean up; it would outlive the caller
    // and only die with the channel. Unregister before rethrowing.
    try {
      this.sendEncryptedMessage(ch, plaintext, requestId)
    }
    catch (err) {
      this.unregisterRequest(ch, requestId)
      this.onSendFailure(ch, err)
      throw err
    }

    return {
      requestId,
      onMessage: (cb) => { messageCb = cb },
      onEnd: (cb) => { endCb = cb },
      onError: (cb) => { errorCb = cb },
    }
  }

  /** Get an open channel for a worker, or open a new one. */
  async getOrOpenChannel(workerId: string): Promise<string> {
    for (const [channelId, ch] of this.channels) {
      // `state === 'verified'` and not merely "not closed": an open in progress has
      // already put its channel here so the verification Ping's reply can route, but
      // that session is unproven. Skipping it drops through to openingChannels.run
      // below, which dedups this caller onto the very same in-flight open -- so a
      // racer waits for the ping instead of being handed the channel the ping might
      // yet reject.
      if (ch.workerId === workerId && ch.state === 'verified') {
        const reason = this.staleReason(ch)
        if (reason) {
          log.debug('reopening stale pooled channel', { channel_id: channelId, worker_id: workerId, reason })
          await this.closeChannel(channelId)
          break
        }
        return channelId
      }
    }

    return this.openingChannels.run(workerId, () => this.openChannel(workerId))
  }

  /**
   * Why a verified pooled channel can no longer be reused, or null if it still
   * can. The three reasons a channel is rotated out of the pool live in one place
   * so the reuse path (and any future caller) share one policy: it aged past
   * CHANNEL_MAX_AGE_MS, its send session needs a rekey, or the identity it was
   * opened under drifted -- this tab logged out and back in as B (or a shared
   * cookie jar was re-authenticated elsewhere) while this up-to-an-hour-old channel
   * is still the one the Hub authenticated as A, and serving it would run every
   * worker RPC B's page issues as A. This is the pooled-read half of the open-time
   * identity cross-check; the Hub stays authoritative and the stale channel is just
   * closed and reopened.
   */
  private staleReason(ch: ActiveChannel): 'expired' | 'needs-rekey' | 'identity-drift' | null {
    if (Date.now() - ch.openedAt > CHANNEL_MAX_AGE_MS) {
      return 'expired'
    }
    if (ch.session.send.needsRekey()) {
      return 'needs-rekey'
    }
    if (this.identityMismatch(this.expectedUserIdFn(), ch.userId)) {
      return 'identity-drift'
    }
    return null
  }

  /**
   * Whether the identity this page expects disagrees with the one the Hub
   * authenticated. An `undefined` `expected` is NOT a mismatch: the page has no
   * expectation yet (e.g. before the auth context resolves) and the Hub stays
   * authoritative. An EMPTY-STRING `expected` IS a mismatch against any non-empty
   * Hub identity: only the "not resolved yet" case (undefined) may skip the check,
   * whereas `''` is a degenerate/corrupt id we must not silently treat as "no
   * expectation" and serve a channel bound to a different user for. `actual` is the
   * Hub-authenticated id, which the Hub never leaves empty. This is the one
   * comparison the open-time reject and the pooled-reuse eviction share, so the
   * skip semantics cannot drift apart.
   */
  private identityMismatch(expected: string | undefined, actual: string): boolean {
    return expected !== undefined && expected !== actual
  }

  /** Check if a channel is open. */
  isOpen(channelId: string): boolean {
    const ch = this.channels.get(channelId)
    return ch !== undefined && ch.state !== 'closed'
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
    for (const ch of this.channels.values()) {
      if (ch.workerId === workerId && ch.state === 'verified')
        return true
    }
    return false
  }

  /** Get the worker ID for a channel. */
  getWorkerId(channelId: string): string | undefined {
    return this.channels.get(channelId)?.workerId
  }

  /** Close all channels and the shared WebSocket. */
  closeAll(): void {
    // First, invalidate every in-flight open (see closeGeneration): the
    // snapshot below cannot see a channel that has not registered yet.
    this.closeGeneration++
    // Snapshot the ids before iterating: closeChannel deletes from this.channels,
    // and a listener it notifies could in principle open a channel mid-loop. ES
    // makes deleting the current entry safe, but a newly-added entry would be
    // visited by a live iterator; iterating a snapshot makes the intent explicit.
    for (const channelId of [...this.channels.keys()])
      void this.closeChannel(channelId)
    this.closeWebSocket()
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
      throw opts.signal.reason instanceof Error ? opts.signal.reason : new ChannelError('client', `RPC call '${method}' aborted`)
    }
    const channelId = await this.getOrOpenChannel(workerId)
    // Re-check after the (potentially async) channel-open: a long
    // handshake gives the caller plenty of time to abort. Skipping
    // this check would still get caught by call()'s pre-check, but
    // checking here saves the protobuf encode round-trip below.
    if (opts?.signal?.aborted) {
      throw opts.signal.reason instanceof Error ? opts.signal.reason : new ChannelError('client', `RPC call '${method}' aborted`)
    }
    const msg = create(reqSchema, req)
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
    log.debug('callWorker response', { method, response: toJsonString(respSchema, result) })
    return result
  }

  /**
   * Remove a stream listener for a specific request on a channel.
   * Called when the client aborts a stream to prevent the old listener
   * from processing events after a stream restart.
   */
  removeStreamListener(channelId: string, requestId: number): void {
    const ch = this.channels.get(channelId)
    if (ch) {
      this.unregisterRequest(ch, requestId)
    }
  }

  // ---- Key pinning utilities ----
  //
  // The whole TOFU key-pinning policy (this section, resolveKeyPin below,
  // rejectedWorkers, and the KeyPin/KeyPinMap types) is a candidate to extract
  // into its own module, mirroring the Go client's KeyPinStore interface
  // (backend/tunnel/channel.go) / tofupins.store implementation -- entanglement
  // with the rest of ChannelManager is limited to three call sites (see
  // openChannel). See https://github.com/leapmux/leapmux/issues/283.

  /** Remove a pinned key for a worker. */
  static clearKeyPin(workerId: string): void {
    const allPins = localStorageGet<KeyPinMap>(KEY_KEY_PINS) ?? {}
    delete allPins[workerId]
    localStorageSet(KEY_KEY_PINS, allPins)
  }

  /** Remove all pinned keys. */
  static clearAllKeyPins(): void {
    localStorageRemove(KEY_KEY_PINS)
  }

  // ---- Private methods ----

  /**
   * Resolve the TOFU key pin for a worker, prompting the user on a mismatch.
   *
   * Returns the `commit` the caller runs once the channel is proven, which records
   * this worker's key. Splitting the decision from the write is what keeps the write
   * correct: `openChannel` awaits the prompt, the handshake, and the WebSocket
   * between the two, and KEY_KEY_PINS holds EVERY worker's pin in one value. Reading
   * the whole map before those awaits and writing it back after would make the open
   * an unserialized read-modify-write over shared state -- and opens to different
   * workers are not serialized (openingChannels is keyed by worker), so two
   * interleaving opens would each write back a map snapshot taken before the other's
   * pin existed, silently dropping it. A dropped pin is not a lost preference: the
   * next open reads no pin, takes the first-use branch, and re-pins whatever key the
   * Hub serves WITHOUT prompting -- exactly the substitution the prompt defends
   * against. So `commit` re-reads the map and mutates only this worker's entry, all
   * synchronously, and no snapshot ever crosses an await.
   *
   * This closes the intra-tab race only; localStorage offers no compare-and-swap, so
   * two browser TABS opening channels at the same instant can still clobber each
   * other's pin. Narrowing the window to a single synchronous block is as far as this
   * API goes.
   *
   * Throws when the user rejects the new key.
   */
  private async resolveKeyPin(workerId: string, keyBundle: WorkerKeyBundle): Promise<() => void> {
    const compositeKeyBytes = concatBytes(keyBundle.x25519PublicKey, keyBundle.mlkemPublicKey, keyBundle.slhdsaPublicKey)
    const publicKeyHex = bytesToHex(compositeKeyBytes)
    const pinned = localStorageGet<KeyPinMap>(KEY_KEY_PINS)?.[workerId] ?? null

    const commit = () => {
      const pins = localStorageGet<KeyPinMap>(KEY_KEY_PINS) ?? {}
      pins[workerId] = { publicKeyHex, firstSeen: Date.now() }
      localStorageSet(KEY_KEY_PINS, pins)
    }

    if (!pinned) {
      // First use: trust and record it.
      return commit
    }

    if (pinned.publicKeyHex === publicKeyHex) {
      // The key we already trust; nothing to write.
      return () => {}
    }

    // Auto-reject if the user already rejected this worker in this session.
    if (this.rejectedWorkers.has(workerId)) {
      throw new ChannelError('client', 'Worker public key rejected by user')
    }

    // Key mismatch — ask user.
    const { keyFingerprintHex } = await import('./fingerprint')
    const decision = await this.transport.confirmKeyPin(
      workerId,
      keyFingerprintHex(pinned.publicKeyHex),
      keyFingerprintHex(publicKeyHex),
    )
    if (decision === 'reject') {
      this.rejectedWorkers.add(workerId)
      throw new ChannelError('client', 'Worker public key rejected by user')
    }
    // User accepted the new key.
    return commit
  }

  /**
   * Fail every waiter on a channel and release its buffers.
   *
   * `streamTermination` differs by caller and is the reason this takes a parameter
   * rather than assuming: a local close is an orderly end its consumer asked for,
   * while a transport failure or a dead session is an error the consumer must see.
   * Callers own removing the channel from `channels`; this only settles what is
   * registered on it.
   */
  private drainChannel(ch: ActiveChannel, err: ChannelError, streamTermination: 'end' | 'error'): void {
    for (const [, pending] of ch.pendingRequests) {
      pending.reject(err)
    }
    ch.pendingRequests.clear()

    for (const [, listener] of ch.streamListeners) {
      if (streamTermination === 'end')
        safeCall(() => listener.onEnd(), 'stream onEnd listener')
      else
        safeCall(() => listener.onError(err), 'stream onError listener')
    }
    ch.streamListeners.clear()

    ch.reassembly.clear()
  }

  /**
   * Decide a channel's fate after sendEncryptedMessage threw.
   *
   * Only a session-level failure kills the channel. `encrypt` throwing means the Noise
   * send state is finished (the nonce ceiling), and a chunked send that threw midway
   * has already put chunks on the wire, leaving the peer's receive nonce ahead of ours
   * -- either way every later send on this channel is garbage. Cancelling it is what
   * lets pooled callers re-resolve onto a fresh one: getOrOpenChannel caches by worker
   * and nothing else evicts a channel before its CHANNEL_MAX_AGE_MS check, so a
   * poisoned session left in the pool would be handed to every later caller and fail
   * identically for up to an hour. The Go client cancels the same way.
   *
   * A `client` ChannelError -- today, a payload over maxMessageSize -- is the opposite
   * case: the session never encrypted a byte and is untouched, so tearing the channel
   * down would punish every other caller for one bad call.
   */
  private onSendFailure(ch: ActiveChannel, err: unknown): void {
    if (err instanceof ChannelError && err.source === 'client')
      return
    log.error('encrypting a channel message failed, closing the channel', { channel_id: ch.channelId, error: err })
    ch.state = 'closed'
    void this.closeChannel(ch.channelId)
  }

  /** Ensure the shared WebSocket is connected. */
  private ensureWebSocket(): Promise<void> {
    // Already connected and open.
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      return Promise.resolve()
    }

    // Connection attempt already in progress - deduplicate.
    if (this.wsPromise) {
      return this.wsPromise
    }

    const ws = this.transport.createWebSocket()

    this.wsPromise = new Promise<void>((resolve, reject) => {
      // Mutual references between timer/onOpen/onError are unavoidable
      // for event handler setup; all are defined before any execute.
      /* eslint-disable ts/no-use-before-define */
      const timer = setTimeout(() => {
        ws.removeEventListener('open', onOpen)
        ws.removeEventListener('error', onError)
        ws.close()
        this.ws = null
        this.wsPromise = null
        reject(new ChannelError('transport', 'WebSocket open timed out after 10s'))
      }, 10_000)

      const onOpen = () => {
        clearTimeout(timer)
        ws.removeEventListener('error', onError)
        this.ws = ws
        this.wsPromise = null

        ws.addEventListener('message', (event: MessageEvent) => {
          // Same stale-socket fence as the close handler below: a superseded
          // socket can still deliver frames it buffered before it was replaced,
          // and routing them into the shared channel map would let a stale
          // CLOSE-flag frame drain a live channel -- or a stale data frame
          // advance a channel's Noise receive nonce -- on the successor's
          // watch. useOrgEvents guards its message handler the same way.
          if (this.ws === ws) {
            this.handleWebSocketMessage(event)
          }
        })

        ws.addEventListener('close', () => {
          // Only the CURRENT socket's close tears the transport down. A stale
          // socket's close fires after readyState already flipped it out of the
          // OPEN fast path above, so a concurrent ensureWebSocket may have
          // opened and installed a successor as this.ws in the window -- acting
          // on the stale close here would drain the successor's channels, null
          // this.ws, and orphan the still-OPEN successor. onOpen/onError capture
          // `ws` for the same reason; the close handler must too.
          if (this.ws === ws) {
            this.handleWebSocketClose()
          }
        })

        resolve()
      }

      const onError = (event: Event) => {
        clearTimeout(timer)
        ws.removeEventListener('open', onOpen)
        this.ws = null
        this.wsPromise = null
        const message = event instanceof ErrorEvent && event.message
          ? event.message
          : 'WebSocket connection failed'
        reject(new ChannelError('transport', message))
      }
      /* eslint-enable ts/no-use-before-define */

      ws.addEventListener('open', onOpen, { once: true })
      ws.addEventListener('error', onError, { once: true })
    })

    return this.wsPromise
  }

  private closeWebSocket(): void {
    if (this.ws) {
      if (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING) {
        this.ws.close(1000, 'closed')
      }
      this.ws = null
    }
    this.wsPromise = null
  }

  /**
   * Encrypt and send plaintext, splitting into chunks if needed.
   * All chunks share the same correlationId. Intermediate chunks have
   * flags=MORE; the final chunk has flags=UNSPECIFIED.
   */
  private sendEncryptedMessage(ch: ActiveChannel, plaintext: Uint8Array, requestId: number): void {
    if (plaintext.length > this.maxMessageSize) {
      throw new ChannelError('client', `message too large: ${plaintext.length} > ${this.maxMessageSize}`)
    }

    if (plaintext.length <= MAX_CHUNK_SIZE) {
      // Single frame — fast path.
      const ciphertext = ch.session.send.encrypt(plaintext)
      this.sendChannelMessage(ch, ciphertext, requestId)
      return
    }

    // Chunked path.
    for (let offset = 0; offset < plaintext.length;) {
      const end = Math.min(offset + MAX_CHUNK_SIZE, plaintext.length)
      const chunk = plaintext.slice(offset, end)
      offset = end

      const ciphertext = ch.session.send.encrypt(chunk)
      const flags = offset < plaintext.length
        ? ChannelMessageFlags.MORE
        : ChannelMessageFlags.UNSPECIFIED
      this.sendChannelMessage(ch, ciphertext, requestId, flags)
    }
  }

  private sendChannelMessage(ch: ActiveChannel, ciphertext: Uint8Array, requestId: number, flags: ChannelMessageFlags = ChannelMessageFlags.UNSPECIFIED): void {
    const msg = create(ChannelMessageSchema, {
      protocolVersion: 1,
      channelId: ch.channelId,
      ciphertext,
      // uint64 on the wire; see the decode boundary in handleMessage for why ids
      // stay plain numbers on this side.
      correlationId: BigInt(requestId),
      flags,
    })
    // Guarded like the receive-side sites (see handleMessage): this runs for
    // every outbound frame -- and once per chunk of a chunked send -- and
    // Logger.debug evaluates its args (a fresh object literal) before checking
    // whether debug logging is on.
    if (log.isDebug())
      log.debug('sending channel message', { channel_id: ch.channelId, correlation_id: requestId })
    const data = toBinary(ChannelMessageSchema, msg)

    // Wire format: [4 bytes big-endian length][protobuf data]
    const buf = new Uint8Array(4 + data.length)
    new DataView(buf.buffer).setUint32(0, data.length)
    buf.set(data, 4)

    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      // Throw rather than log-and-return: call()/stream() wrap the send in a
      // try/catch that unregisters the just-registered pending request /
      // stream listener and rejects the caller. Swallowing this here would
      // leave that request live until its ~15s RPC timeout (or, for a stream,
      // forever) if the close event that would otherwise drain it is delayed or
      // superseded by a successor socket. A non-'client' source makes
      // onSendFailure tear the pooled channel down so the next call re-opens.
      // this.ws is only ever an OPEN-or-later socket (it is assigned in onOpen),
      // so a non-OPEN readyState here means CLOSING/CLOSED -- a dead transport.
      throw new ChannelError('transport', 'cannot send channel message: WebSocket not open')
    }
    this.ws.send(buf)
  }

  /** Normalize WebSocket message data and route to the correct channel. */
  private handleWebSocketMessage(event: MessageEvent): void {
    const raw = event.data
    let ab: ArrayBuffer
    if (raw instanceof ArrayBuffer) {
      ab = raw
    }
    else if (ArrayBuffer.isView(raw)) {
      // Node.js WebSocket may return Buffer instead of ArrayBuffer.
      ab = raw.buffer.slice(raw.byteOffset, raw.byteOffset + raw.byteLength) as ArrayBuffer
    }
    else {
      return
    }
    this.handleMultiplexedMessage(ab)
  }

  private handleMultiplexedMessage(data: ArrayBuffer): void {
    const buf = new Uint8Array(data)
    // A framing violation is dropped, but never silently: a systematic
    // Hub<->browser framing desync would otherwise surface only as
    // unexplained RPC timeouts, unlike every other rejection in this file,
    // which logs.
    if (buf.length < 4) {
      log.warn('dropping WebSocket frame shorter than its length prefix', { length: buf.length })
      return
    }

    const length = new DataView(buf.buffer, buf.byteOffset).getUint32(0)
    if (length !== buf.length - 4) {
      log.warn('dropping WebSocket frame with a mismatched length prefix', { declared: length, actual: buf.length - 4 })
      return
    }

    // Zero-copy view past the 4-byte length prefix. fromBinary decodes
    // synchronously and protobuf-es aliases the `ciphertext` bytes field as a
    // subarray of this input; both are safe because each inbound WS frame owns a
    // fresh, never-reused ArrayBuffer and ciphertext is consumed (decrypt /
    // hub-control parse) before the frame is dropped -- so no copy is needed.
    const msg: ChannelMessage = fromBinary(ChannelMessageSchema, buf.subarray(4))

    if (msg.channelId === HUB_CONTROL_CHANNEL_ID) {
      this.handleHubControl(msg)
      return
    }

    this.handleMessage(msg.channelId, msg)
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

  private handleMessage(channelId: string, msg: ChannelMessage): void {
    const ch = this.channels.get(channelId)
    if (!ch)
      return

    // Guard the payload build behind isDebug: handleMessage runs for every inbound
    // frame (RPC response, stream chunk, tunnel data/credit), and the debug args --
    // a fresh object literal plus a bigint->string conversion -- are evaluated at the
    // call site regardless of whether debug logging is on (see Logger.debug).
    if (log.isDebug())
      log.debug('received channel message', { channel_id: channelId, correlation_id: String(msg.correlationId) })

    // Close sentinel: CLOSE flag.
    if (msg.flags === ChannelMessageFlags.CLOSE) {
      this.drainChannel(ch, new ChannelError('transport', 'channel closed by server'), 'error')
      ch.state = 'closed'
      this.channels.delete(channelId)
      this.notifyStateChange()
      return
    }

    // Decrypt the ciphertext.
    let decrypted: Uint8Array
    try {
      decrypted = ch.session.receive.decrypt(msg.ciphertext)
    }
    catch (err) {
      log.error('Failed to decrypt channel message, closing channel', { channel_id: channelId, error: err })
      void this.closeChannel(channelId)
      return
    }

    // correlation_id is uint64 on the wire so the id space cannot wrap (see
    // channel.proto), which protobuf-es surfaces as a bigint. Convert once here
    // rather than making every request registry, stream listener, and reassembly key
    // a bigint: ids are allocated as a plain counter and never approach 2^53, where a
    // JS number stops being exact -- at the ~640 ids/sec a saturated tunnel burns,
    // reaching it takes ~450,000 years.
    //
    // A value past the safe range is therefore not one this client ever allocated, so
    // routing it would mean rounding it onto some OTHER request's handler. Drop the
    // message instead.
    //
    // This runs AFTER the decrypt, and must: Noise nonces are implicit and sequential,
    // so skipping a ciphertext leaves our receive nonce behind the peer's send nonce
    // and every subsequent message fails to decrypt. Dropping the plaintext costs one
    // message; dropping the ciphertext would cost the channel.
    if (msg.correlationId > MAX_SAFE_CORRELATION_ID) {
      log.error('dropping channel message with an out-of-range correlation id', {
        channel_id: channelId,
        correlation_id: String(msg.correlationId),
      })
      return
    }
    const correlationId = Number(msg.correlationId)

    // An out-of-spec flags value (e.g. MORE|CLOSE combined, which no
    // conformant sender emits) is a protocol violation dropped here rather
    // than misread as "final chunk" -- which would hand a truncated assembly
    // to the InnerMessage decoder. Mirrors the Go receivers
    // (channelwire.ChunkContinuation); CLOSE was already handled above. Runs
    // after the decrypt so the drop does not desync the receive nonce.
    if (msg.flags !== ChannelMessageFlags.UNSPECIFIED && msg.flags !== ChannelMessageFlags.MORE) {
      log.warn('dropping channel message with out-of-spec flags', {
        channel_id: channelId,
        correlation_id: correlationId,
        flags: msg.flags,
      })
      return
    }

    // Feed the frame through chunk reassembly. A null result means it did not
    // complete a message (a buffered MORE chunk, or a dropped chunk for a
    // poisoned/unknown/over-cap id, or an oversize breach); a complete message
    // returns its full plaintext. Test with `=== null`, NOT falsiness: a zero-length
    // payload is a valid complete message that must still dispatch.
    const plaintext = this.reassemble(ch, correlationId, msg.flags, decrypted)
    if (plaintext === null)
      return

    // Deserialize the InnerMessage envelope.
    let envelope
    try {
      envelope = fromBinary(InnerMessageSchema, plaintext)
    }
    catch (err) {
      log.error('Failed to deserialize InnerMessage', err)
      return
    }

    switch (envelope.kind.case) {
      case 'response':
        this.deliverResponse(ch, correlationId, envelope.kind.value)
        break
      case 'stream':
        this.deliverStream(ch, correlationId, envelope.kind.value)
        break
      default:
        log.warn('Unknown inner message type', envelope.kind.case)
    }
  }

  /**
   * Feed one decrypted frame into the correlation id's reassembly state and return
   * the complete message plaintext, or null when this frame did not complete one --
   * a buffered MORE chunk, a dropped chunk (poisoned/unknown/over-cap id), or an
   * oversize breach. A single non-chunked frame returns its own bytes unchanged.
   * Callers MUST test the result with `=== null`, not falsiness: a zero-length
   * payload is a valid complete message.
   *
   * The DECISION (buffer / drop / breach / deliver) lives in Reassembler.accept;
   * this method is the DISPATCH -- logging with channel context, rejecting the
   * owning request on a breach, returning the plaintext on a deliver -- mirroring
   * the Go split (reassembleLocked decides under the lock, reassemble dispatches
   * outside it). hasHandler lets accept check the handler registry without
   * Reassembler knowing about the ChannelManager's request/stream maps.
   */
  private reassemble(
    ch: ActiveChannel,
    correlationId: number,
    flags: ChannelMessageFlags,
    decrypted: Uint8Array,
  ): Uint8Array | null {
    const out = ch.reassembly.accept(
      correlationId,
      decrypted,
      flags === ChannelMessageFlags.MORE,
      id => ch.pendingRequests.has(id) || ch.streamListeners.has(id),
    )
    switch (out.kind) {
      case 'deliver':
        return out.plaintext
      case 'buffered':
      case 'drop-poisoned':
        return null
      case 'drop-unknown':
        log.warn('dropped chunk for an unknown correlation id', { channel_id: ch.channelId, correlation_id: correlationId })
        return null
      case 'too-many':
        // The in-flight cap and the size cap are distinct outcomes, so the log
        // wording and the rejection message are chosen by kind -- not by
        // string-matching a message accept produced.
        log.error('Too many incomplete chunked messages', { channel_id: ch.channelId, correlation_id: correlationId })
        this.failReassembly(ch, correlationId, 'too many incomplete chunked messages')
        return null
      case 'too-large':
        log.error('Chunked message exceeds max size', { channel_id: ch.channelId, correlation_id: correlationId, size: out.size })
        this.failReassembly(ch, correlationId, `chunked message too large: ${out.size} bytes exceeds ${this.maxMessageSize} byte limit`)
        return null
    }
  }

  /** Route a completed InnerRpcResponse to its pending request. */
  private deliverResponse(ch: ActiveChannel, correlationId: number, resp: InnerRpcResponse): void {
    // Guard the payload build behind isDebug, as handleMessage does: this runs
    // once per inbound RPC response, and the object literal (plus the payload
    // length read) is evaluated at the call site regardless of whether debug
    // logging is on (see Logger.debug).
    if (log.isDebug()) {
      log.debug('received inner RPC response', {
        channel_id: ch.channelId,
        correlation_id: correlationId,
        is_error: resp.isError,
        error_code: resp.errorCode,
        error_message: resp.errorMessage,
        payload_len: resp.payload.length,
      })
    }
    const pending = ch.pendingRequests.get(correlationId)
    if (pending) {
      this.unregisterRequest(ch, correlationId)
      if (resp.isError) {
        const err = new ChannelError('rpc', resp.errorMessage || `RPC error code ${resp.errorCode}`, resp.errorCode)
        this.notifyError(ch.workerId, err)
        pending.reject(err)
      }
      else {
        pending.resolve(resp)
      }
      return
    }

    // A unary reply on a correlation id we registered as a STREAM. Without
    // this arm the frame is dropped and the subscription waits forever
    // with no error to retry from, which is a silent dead tab.
    //
    // It cannot be fixed purely by registering every streaming method as
    // streaming on the worker: some of these replies come from places
    // that cannot know the method's shape at all -- the dispatcher's
    // Unimplemented answer for a method it has no registration for is the
    // clearest case. This is the one point every inbound reply passes, so
    // the fallback belongs here. Errors only: a non-error unary payload on
    // a stream id is not something a listener can interpret, and dropping
    // it stays the safe reading.
    if (!resp.isError) {
      return
    }
    const listener = ch.streamListeners.get(correlationId)
    if (!listener) {
      return
    }
    this.unregisterRequest(ch, correlationId)
    const err = new ChannelError('rpc', resp.errorMessage || `RPC error code ${resp.errorCode}`, resp.errorCode)
    this.notifyError(ch.workerId, err)
    // safeCall for the same reason rejectPendingRequest uses it: a throwing
    // app callback must not unwind back through handleMessage.
    safeCall(() => listener.onError(err), 'stream onError listener')
  }

  /** Route an InnerStreamMessage to its stream listener. */
  private deliverStream(ch: ActiveChannel, correlationId: number, streamMsg: InnerStreamMessage): void {
    // Guard the payload build behind isDebug, as handleMessage does: this runs
    // once per inbound stream frame (the per-chunk hot path), and the object
    // literal (plus the payload length read) is evaluated at the call site
    // regardless of whether debug logging is on (see Logger.debug).
    if (log.isDebug()) {
      log.debug('received inner stream message', {
        channel_id: ch.channelId,
        correlation_id: correlationId,
        end: streamMsg.end,
        is_error: streamMsg.isError,
        error_code: streamMsg.errorCode,
        error_message: streamMsg.errorMessage,
        payload_len: streamMsg.payload.length,
      })
    }
    const listener = ch.streamListeners.get(correlationId)
    if (listener) {
      // Unregister BEFORE invoking the terminal callback, and isolate every
      // listener call with safeCall, mirroring drainChannel: a throwing app
      // callback must not skip unregisterRequest (which would pin the stream's
      // reassembly buffer and its incomplete-chunked cap slot for the channel's
      // life) or unwind into the WebSocket message dispatch.
      if (streamMsg.isError) {
        const err = new ChannelError('stream', streamMsg.errorMessage || `stream error code ${streamMsg.errorCode}`, streamMsg.errorCode)
        this.notifyError(ch.workerId, err)
        this.unregisterRequest(ch, correlationId)
        safeCall(() => listener.onError(err), 'stream onError listener')
      }
      else if (streamMsg.end) {
        this.unregisterRequest(ch, correlationId)
        safeCall(() => listener.onEnd(), 'stream onEnd listener')
      }
      else {
        safeCall(() => listener.onMessage(streamMsg), 'stream onMessage listener')
      }
    }
  }

  /**
   * Drop every handler registered for a request and, with them, its reassembly buffer.
   *
   * A correlation id is either a pending unary or a stream, never both, so clearing
   * both maps is safe and keeps the rule -- a reassembly buffer lives and dies with the
   * request that owns it -- in ONE place instead of at each of the six sites that
   * retire a request. Left behind, a partial reassembly pins up to maxMessageSize and
   * holds a slot of the incomplete-chunked cap for the channel's whole life, because
   * nothing else ever reaps it: the buffer's only reader was the handler just removed.
   */
  private unregisterRequest(ch: ActiveChannel, correlationId: number): void {
    ch.pendingRequests.delete(correlationId)
    ch.streamListeners.delete(correlationId)
    ch.reassembly.drop(correlationId)
  }

  /**
   * Report a reassembly-limit breach to the request that owns the id, then tombstone
   * the id so the rest of its chunks are dropped in silence.
   *
   * Order matters: reporting unregisters the request, which reaps its buffer, so
   * poisoning first would leave nothing behind. Poisoning after is what keeps chunks
   * 2..N of the rejected message RECOGNISED — a 16 MiB message is ~256 frames, and
   * without the tombstone each one re-enters the unknown-id path and logs.
   */
  private failReassembly(ch: ActiveChannel, correlationId: number, message: string): void {
    this.rejectPendingRequest(ch, correlationId, 'client', message)
    ch.reassembly.poison(correlationId)
  }

  /** Reject a pending request or error an active stream. */
  private rejectPendingRequest(ch: ActiveChannel, correlationId: number, source: ChannelErrorSource, message: string): void {
    const pending = ch.pendingRequests.get(correlationId)
    if (pending) {
      this.unregisterRequest(ch, correlationId)
      pending.reject(new ChannelError(source, message))
      return
    }
    const listener = ch.streamListeners.get(correlationId)
    if (listener) {
      this.unregisterRequest(ch, correlationId)
      // Isolate the app callback: failReassembly poisons the id AFTER this returns,
      // and a throwing onError would unwind out of failReassembly -> reassemble ->
      // handleMessage, skipping the poison. The id would then be un-tombstoned (its
      // buffer already reaped by unregisterRequest above), so every remaining chunk
      // of the rejected message re-enters the unknown-id path and logs -- the exact
      // per-chunk storm the tombstone exists to prevent. safeCall matches the
      // isolation deliverStream/drainChannel already apply to onError elsewhere.
      safeCall(() => listener.onError(new ChannelError(source, message)), 'stream onError listener')
    }
  }

  /** Handle shared WebSocket close: tear down all channels. */
  private handleWebSocketClose(): void {
    this.ws = null

    // A successor dial started in the gap between this socket's readyState leaving
    // OPEN and this queued close event firing owns this.wsPromise: the socket that
    // just closed nulled wsPromise in its own onOpen, so a non-null promise here can
    // only belong to a newer ensureWebSocket. Nulling it -- or clearing the
    // per-worker open dedup -- would orphan that successor's still-dialing socket (an
    // idle Hub connection once it opens) and let a duplicate channel-open start. This
    // completes the `this.ws === ws` guard at the close listener, which already skips
    // a close whose successor has fully OPENED but not one still DIALING.
    const successorDialing = this.wsPromise !== null

    for (const channelId of [...this.channels.keys()]) {
      const ch = this.channels.get(channelId)
      if (!ch)
        continue
      this.drainChannel(ch, new ChannelError('transport', 'channel disconnected'), 'error')
      ch.state = 'closed'
      this.channels.delete(channelId)
    }

    if (!successorDialing) {
      this.wsPromise = null
      this.openingChannels.clear()
    }
    this.notifyStateChange()
  }
}
