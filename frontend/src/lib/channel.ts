/**
 * Encrypted channel manager for E2EE communication with Workers.
 *
 * Manages the lifecycle of encrypted channels:
 *   1. Fetch Worker's public key via ChannelTransport.getWorkerPublicKey
 *   2. Check key pinning (TOFU model) — prompt user on mismatch
 *   3. Perform Noise_NK handshake via ChannelTransport.openChannel
 *   4. Send UserIdClaim as first encrypted message, wait for verification
 *   5. Connect a single shared WebSocket relay for all encrypted traffic
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
import {
  ChannelMessageFlags,
  ChannelMessageSchema,
  EncryptionMode,
  HubControlFrameSchema,
  InnerMessageSchema,
  InnerRpcRequestSchema,
  UserIdClaimSchema,
} from '~/generated/leapmux/v1/channel_pb'
import { createLogger } from './logger'
import { initiatorHandshake1 as classicHandshake1, initiatorHandshake2 as classicHandshake2, concatBytes } from './noise'
import { initiatorHandshake1, initiatorHandshake2 } from './noise-hybrid'
import { safeGetJson, safeRemoveItem, safeSetJson } from './safeStorage'

const log = createLogger('channel')

/** Reserved channel ID for Hub-originated control frames. */
const HUB_CONTROL_CHANNEL_ID = '_hub'

export type KeyPinDecision = 'accept' | 'reject'

/**
 * Structured error for channel operations.
 * - `transport`: WebSocket disconnect/timeout, channel closed by server (connection-level)
 * - `stream`: Backend stream error (carries backend error code)
 * - `rpc`: Backend RPC error (carries backend error code)
 * - `client`: Client-side issues (channel not open, message too large, claim rejected)
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

/** Transport interface for platform-specific RPC and WebSocket creation. */
export interface ChannelTransport {
  getWorkerPublicKey: (workerId: string) => Promise<WorkerKeyBundle>
  getWorkerEncryptionMode: (workerId: string) => Promise<EncryptionMode>
  openChannel: (workerId: string, handshakePayload: Uint8Array) => Promise<{ channelId: string, handshakePayload: Uint8Array }>
  closeChannel: (channelId: string) => Promise<void>
  createWebSocket: () => WebSocket
  /** Called when a previously-pinned worker public key changes. */
  confirmKeyPin: (workerId: string, expectedFingerprint: string, actualFingerprint: string) => Promise<KeyPinDecision>
  /** Returns the current user ID for the UserIdClaim post-handshake check. */
  getUserId: () => string
}

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

/** Maximum plaintext bytes per Noise transport message (65535 - 16 byte auth tag). */
const MAX_CHUNK_SIZE = 65535 - 16

/** Default maximum reassembled message size (16 MiB). */
const DEFAULT_MAX_MESSAGE_SIZE = 16 * 1024 * 1024

/** Maximum number of in-flight chunked sequences per channel. */
const MAX_INCOMPLETE_CHUNKED = 4

interface ChunkBuffer {
  parts: Uint8Array[]
  total: number
}

interface ActiveChannel {
  channelId: string
  workerId: string
  session: Session
  pendingRequests: Map<number, PendingRequest>
  streamListeners: Map<number, StreamListener>
  reassembly: Map<number, ChunkBuffer>
  nextRequestId: number
  closed: boolean
  openedAt: number
  /** Pending claim verification resolve/reject. */
  claimResolve?: () => void
  claimReject?: (err: Error) => void
}

/** Default RPC call timeout in milliseconds (matches hub apiTimeoutSeconds). */
const DEFAULT_RPC_TIMEOUT_MS = 10_000

/** Optional overrides for testing (dependency injection). */
export interface ChannelManagerOpts {
  handshake1?: typeof initiatorHandshake1
  handshake2?: typeof initiatorHandshake2
  classicHandshake1?: typeof classicHandshake1
  classicHandshake2?: typeof classicHandshake2
  maxMessageSize?: number
  /** Timeout for individual RPC calls in milliseconds. Defaults to 30s. */
  rpcTimeout?: number
}

/** ChannelManager manages encrypted E2EE channels to Workers. */
export class ChannelManager {
  private transport: ChannelTransport
  private channels = new Map<string, ActiveChannel>()
  /** In-flight openChannel promises per worker, for deduplication. */
  private openingChannels = new Map<string, Promise<string>>()
  private ws: WebSocket | null = null
  private wsPromise: Promise<void> | null = null
  private handshake1: typeof initiatorHandshake1
  private handshake2: typeof initiatorHandshake2
  private classicHS1: typeof classicHandshake1
  private classicHS2: typeof classicHandshake2
  private maxMessageSize: number
  private rpcTimeout: number
  /** Workers whose keys were rejected by the user during this session. */
  private rejectedWorkers = new Set<string>()

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
    this.rpcTimeout = opts?.rpcTimeout ?? DEFAULT_RPC_TIMEOUT_MS
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

  /** Check if any non-closed channel exists for a worker. */
  hasOpenChannel(workerId: string): boolean {
    for (const ch of this.channels.values()) {
      if (ch.workerId === workerId && !ch.closed)
        return true
    }
    return false
  }

  private notifyStateChange(): void {
    for (const cb of this.stateListeners) cb()
  }

  private notifyError(workerId: string, error: ChannelError): void {
    for (const cb of this.errorListeners) cb(workerId, error)
  }

  /**
   * Open an encrypted channel to a Worker.
   * Performs the Noise_NK handshake, key pinning check, UserIdClaim verification,
   * and connects the shared WebSocket relay.
   */
  async openChannel(workerId: string): Promise<string> {
    // 1. Get Worker's encryption mode from live connection, then fetch keys.
    const mode = await this.transport.getWorkerEncryptionMode(workerId)
    const keyBundle = await this.transport.getWorkerPublicKey(workerId)

    // 2. Key pinning (TOFU model) — pin composite key.
    const compositeKeyBytes = concatBytes(keyBundle.x25519PublicKey, keyBundle.mlkemPublicKey, keyBundle.slhdsaPublicKey)
    const publicKeyHex = bytesToHex(compositeKeyBytes)
    const allPins = safeGetJson<Record<string, { publicKeyHex: string, firstSeen: number }>>('leapmux:key-pins') ?? {}
    const pinned = allPins[workerId] ?? null

    if (pinned && pinned.publicKeyHex !== publicKeyHex) {
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
      allPins[workerId] = { publicKeyHex, firstSeen: Date.now() }
      safeSetJson('leapmux:key-pins', allPins)
    }

    // 3. Perform handshake based on encryption mode.
    let session: Session
    let result: { channelId: string, handshakePayload: Uint8Array }

    if (mode === EncryptionMode.CLASSIC) {
      // Classical Noise_NK (X25519 only).
      const hs = this.classicHS1(keyBundle.x25519PublicKey)
      result = await this.transport.openChannel(workerId, hs.message1)
      session = this.classicHS2(hs.handshakeState, result.handshakePayload)
    }
    else {
      // Post-quantum hybrid Noise_NK (X25519 + ML-KEM + SLH-DSA).
      const hs = this.handshake1(keyBundle.x25519PublicKey, keyBundle.mlkemPublicKey)
      result = await this.transport.openChannel(workerId, hs.message1)
      session = this.handshake2(hs.handshakeState, result.handshakePayload, keyBundle.slhdsaPublicKey)
    }

    // 4. Ensure shared WebSocket is connected.
    await this.ensureWebSocket()

    const channel: ActiveChannel = {
      channelId: result.channelId,
      workerId,
      session,
      pendingRequests: new Map(),
      streamListeners: new Map(),
      reassembly: new Map(),
      nextRequestId: 1,
      closed: false,
      openedAt: Date.now(),
    }

    this.channels.set(result.channelId, channel)

    // 5. Pin key on first use (TOFU).
    if (!pinned) {
      allPins[workerId] = { publicKeyHex, firstSeen: Date.now() }
      safeSetJson('leapmux:key-pins', allPins)
    }

    // 6. Send UserIdClaim as first encrypted message.
    try {
      await this.sendUserIdClaim(channel)
    }
    catch (err) {
      // Claim failed — close channel.
      channel.closed = true
      this.channels.delete(result.channelId)
      throw err
    }

    this.notifyStateChange()
    return result.channelId
  }

  /** Close an encrypted channel (does not close the shared WebSocket). */
  async closeChannel(channelId: string): Promise<void> {
    const ch = this.channels.get(channelId)
    if (!ch)
      return

    ch.closed = true

    // Reject pending requests.
    for (const [, pending] of ch.pendingRequests) {
      pending.reject(new ChannelError('client', 'channel closed'))
    }
    ch.pendingRequests.clear()

    // End active streams.
    for (const [, listener] of ch.streamListeners) {
      listener.onEnd()
    }
    ch.streamListeners.clear()
    ch.reassembly.clear()

    // Reject pending claim verification.
    ch.claimReject?.(new ChannelError('client', 'channel closed'))
    ch.claimResolve = undefined
    ch.claimReject = undefined

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

  /** Send a unary RPC request through the encrypted channel. */
  call(channelId: string, method: string, payload: Uint8Array): Promise<InnerRpcResponse> {
    const ch = this.channels.get(channelId)
    if (!ch || ch.closed) {
      return Promise.reject(new ChannelError('client', 'channel not open'))
    }

    const requestId = ch.nextRequestId++

    return new Promise<InnerRpcResponse>((resolve, reject) => {
      const timeoutSec = Math.round(this.rpcTimeout / 1000)
      const timer = setTimeout(() => {
        ch.pendingRequests.delete(requestId)
        log.debug('inner RPC request timed out', { channel_id: ch.channelId, id: requestId, method })
        reject(new ChannelError('client', `RPC call '${method}' timed out after ${timeoutSec}s (channel=${channelId})`))
      }, this.rpcTimeout)

      log.debug('sending inner RPC request', { channel_id: ch.channelId, id: requestId, method, payload_len: payload.length })

      ch.pendingRequests.set(requestId, {
        resolve: (resp) => {
          clearTimeout(timer)
          resolve(resp)
        },
        reject: (err) => {
          clearTimeout(timer)
          reject(err)
        },
      })

      const innerReq = create(InnerRpcRequestSchema, {
        method,
        payload,
      })

      const envelope = create(InnerMessageSchema, {
        kind: { case: 'request', value: innerReq },
      })
      const plaintext = toBinary(InnerMessageSchema, envelope)
      this.sendEncryptedMessage(ch, plaintext, requestId)
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
    if (!ch || ch.closed) {
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

    const innerReq = create(InnerRpcRequestSchema, {
      method,
      payload,
    })

    const envelope = create(InnerMessageSchema, {
      kind: { case: 'request', value: innerReq },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    this.sendEncryptedMessage(ch, plaintext, requestId)

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
      if (ch.workerId === workerId && !ch.closed) {
        // Check time-based expiry.
        if (Date.now() - ch.openedAt > CHANNEL_MAX_AGE_MS) {
          await this.closeChannel(channelId)
          break
        }
        // Check nonce-based rekey need.
        if (ch.session.send.needsRekey()) {
          await this.closeChannel(channelId)
          break
        }
        return channelId
      }
    }

    // Deduplicate concurrent openChannel calls for the same worker.
    const inflight = this.openingChannels.get(workerId)
    if (inflight)
      return inflight

    const promise = this.openChannel(workerId).finally(() => {
      this.openingChannels.delete(workerId)
    })
    this.openingChannels.set(workerId, promise)
    return promise
  }

  /** Check if a channel is open. */
  isOpen(channelId: string): boolean {
    const ch = this.channels.get(channelId)
    return ch !== undefined && !ch.closed
  }

  /** Get the worker ID for a channel. */
  getWorkerId(channelId: string): string | undefined {
    return this.channels.get(channelId)?.workerId
  }

  /** Close all channels and the shared WebSocket. */
  closeAll(): void {
    for (const [channelId] of this.channels) {
      void this.closeChannel(channelId)
    }
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
  ): Promise<MessageShape<RespSchema>> {
    const channelId = await this.getOrOpenChannel(workerId)
    const msg = create(reqSchema, req)
    log.debug('callWorker request', { method, request: toJsonString(reqSchema, msg) })
    const payload = toBinary(reqSchema, msg)
    let resp
    try {
      resp = await this.call(channelId, method, payload)
    }
    catch (err) {
      log.debug('callWorker error', { method, error: err instanceof Error ? err.message : String(err) })
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
      ch.streamListeners.delete(requestId)
    }
  }

  // ---- Key pinning utilities ----

  /** Remove a pinned key for a worker. */
  static clearKeyPin(workerId: string): void {
    const allPins = safeGetJson<Record<string, { publicKeyHex: string, firstSeen: number }>>('leapmux:key-pins') ?? {}
    delete allPins[workerId]
    if (Object.keys(allPins).length > 0) {
      safeSetJson('leapmux:key-pins', allPins)
    }
    else {
      safeRemoveItem('leapmux:key-pins')
    }
  }

  /** Remove all pinned keys. */
  static clearAllKeyPins(): void {
    safeRemoveItem('leapmux:key-pins')
  }

  // ---- Private methods ----

  /** Send a UserIdClaim and wait for the response. */
  private sendUserIdClaim(ch: ActiveChannel): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      ch.claimResolve = resolve
      ch.claimReject = reject

      const claim = create(UserIdClaimSchema, {
        userId: this.transport.getUserId(),
        timestampMs: BigInt(Date.now()),
      })

      const envelope = create(InnerMessageSchema, {
        kind: { case: 'userIdClaim', value: claim },
      })
      const plaintext = toBinary(InnerMessageSchema, envelope)
      this.sendEncryptedMessage(ch, plaintext, 0)
    })
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
          this.handleWebSocketMessage(event)
        })

        ws.addEventListener('close', () => {
          this.handleWebSocketClose()
        })

        resolve()
      }

      const onError = () => {
        clearTimeout(timer)
        ws.removeEventListener('open', onOpen)
        this.ws = null
        this.wsPromise = null
        reject(new ChannelError('transport', 'WebSocket connection failed'))
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
      correlationId: requestId,
      flags,
    })
    log.debug('sending channel message', { channel_id: ch.channelId, correlation_id: requestId })
    const data = toBinary(ChannelMessageSchema, msg)

    // Wire format: [4 bytes big-endian length][protobuf data]
    const buf = new Uint8Array(4 + data.length)
    new DataView(buf.buffer).setUint32(0, data.length)
    buf.set(data, 4)

    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      log.error('Cannot send channel message: WebSocket not open')
      return
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
    if (buf.length < 4)
      return

    const length = new DataView(buf.buffer, buf.byteOffset).getUint32(0)
    if (length !== buf.length - 4)
      return

    const msg: ChannelMessage = fromBinary(ChannelMessageSchema, buf.slice(4))

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

    log.debug('received channel message', { channel_id: channelId, correlation_id: msg.correlationId })

    // Close sentinel: CLOSE flag.
    if (msg.flags === ChannelMessageFlags.CLOSE) {
      for (const [, pending] of ch.pendingRequests) {
        pending.reject(new ChannelError('transport', 'channel closed by server'))
      }
      ch.pendingRequests.clear()
      for (const [, listener] of ch.streamListeners) {
        listener.onError(new ChannelError('transport', 'channel closed by server'))
      }
      ch.streamListeners.clear()
      ch.reassembly.clear()
      ch.claimReject?.(new ChannelError('transport', 'channel closed by server'))
      ch.claimResolve = undefined
      ch.claimReject = undefined
      ch.closed = true
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
      log.error('Failed to decrypt channel message', err)
      return
    }

    // Handle chunked reassembly.
    let plaintext: Uint8Array
    if (msg.flags === ChannelMessageFlags.MORE) {
      // More chunks to come — buffer this one.
      let buf = ch.reassembly.get(msg.correlationId)
      if (!buf) {
        if (ch.reassembly.size >= MAX_INCOMPLETE_CHUNKED) {
          log.error('Too many incomplete chunked messages', { channel_id: channelId, correlation_id: msg.correlationId })
          this.rejectPendingRequest(ch, msg.correlationId, 'client', 'too many incomplete chunked messages')
          return
        }
        buf = { parts: [], total: 0 }
        ch.reassembly.set(msg.correlationId, buf)
      }
      buf.parts.push(decrypted)
      buf.total += decrypted.length
      if (buf.total > this.maxMessageSize) {
        log.error('Chunked message exceeds max size', { channel_id: channelId, correlation_id: msg.correlationId, size: buf.total })
        ch.reassembly.delete(msg.correlationId)
        const errMsg = `chunked message too large: ${buf.total} bytes exceeds ${this.maxMessageSize} byte limit`
        this.rejectPendingRequest(ch, msg.correlationId, 'client', errMsg)
        return
      }
      return
    }

    // Final chunk or single non-chunked message.
    const buf = ch.reassembly.get(msg.correlationId)
    if (buf) {
      buf.parts.push(decrypted)
      buf.total += decrypted.length
      if (buf.total > this.maxMessageSize) {
        log.error('Chunked message exceeds max size', { channel_id: channelId, correlation_id: msg.correlationId, size: buf.total })
        ch.reassembly.delete(msg.correlationId)
        const errMsg = `chunked message too large: ${buf.total} bytes exceeds ${this.maxMessageSize} byte limit`
        this.rejectPendingRequest(ch, msg.correlationId, 'client', errMsg)
        return
      }
      const full = new Uint8Array(buf.total)
      let offset = 0
      for (const part of buf.parts) {
        full.set(part, offset)
        offset += part.length
      }
      plaintext = full
      ch.reassembly.delete(msg.correlationId)
    }
    else {
      plaintext = decrypted
    }

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
      case 'response': {
        const resp = envelope.kind.value
        log.debug('received inner RPC response', {
          channel_id: channelId,
          correlation_id: msg.correlationId,
          is_error: resp.isError,
          error_code: resp.errorCode,
          error_message: resp.errorMessage,
          payload_len: resp.payload.length,
        })
        const pending = ch.pendingRequests.get(msg.correlationId)
        if (pending) {
          ch.pendingRequests.delete(msg.correlationId)
          if (resp.isError) {
            const err = new ChannelError('rpc', resp.errorMessage || `RPC error code ${resp.errorCode}`, resp.errorCode)
            this.notifyError(ch.workerId, err)
            pending.reject(err)
          }
          else {
            pending.resolve(resp)
          }
        }
        break
      }

      case 'stream': {
        const streamMsg = envelope.kind.value
        log.debug('received inner stream message', {
          channel_id: channelId,
          correlation_id: msg.correlationId,
          end: streamMsg.end,
          is_error: streamMsg.isError,
          error_code: streamMsg.errorCode,
          error_message: streamMsg.errorMessage,
          payload_len: streamMsg.payload.length,
        })
        const listener = ch.streamListeners.get(msg.correlationId)
        if (listener) {
          if (streamMsg.isError) {
            const err = new ChannelError('stream', streamMsg.errorMessage || `stream error code ${streamMsg.errorCode}`, streamMsg.errorCode)
            this.notifyError(ch.workerId, err)
            listener.onError(err)
            ch.streamListeners.delete(msg.correlationId)
          }
          else if (streamMsg.end) {
            listener.onEnd()
            ch.streamListeners.delete(msg.correlationId)
          }
          else {
            listener.onMessage(streamMsg)
          }
        }
        break
      }

      case 'userIdClaimResponse': {
        const claimResp = envelope.kind.value
        log.debug('received user_id_claim_response', { channel_id: channelId, correlation_id: msg.correlationId, success: claimResp.success })
        if (!claimResp.success) {
          ch.closed = true
          this.channels.delete(channelId)
          ch.claimReject?.(new ChannelError('client', claimResp.errorMessage || 'User ID claim rejected'))
        }
        else {
          ch.claimResolve?.()
        }
        ch.claimResolve = undefined
        ch.claimReject = undefined
        break
      }

      default:
        log.warn('Unknown inner message type', envelope.kind.case)
    }
  }

  /** Reject a pending request or error an active stream. */
  private rejectPendingRequest(ch: ActiveChannel, correlationId: number, source: ChannelErrorSource, message: string): void {
    const pending = ch.pendingRequests.get(correlationId)
    if (pending) {
      ch.pendingRequests.delete(correlationId)
      pending.reject(new ChannelError(source, message))
      return
    }
    const listener = ch.streamListeners.get(correlationId)
    if (listener) {
      ch.streamListeners.delete(correlationId)
      listener.onError(new ChannelError(source, message))
    }
  }

  /** Handle shared WebSocket close: tear down all channels. */
  private handleWebSocketClose(): void {
    this.ws = null
    this.wsPromise = null

    for (const [channelId, ch] of this.channels) {
      // Reject all pending requests.
      for (const [, pending] of ch.pendingRequests) {
        pending.reject(new ChannelError('transport', 'channel disconnected'))
      }
      ch.pendingRequests.clear()

      // End all streams.
      for (const [, listener] of ch.streamListeners) {
        listener.onError(new ChannelError('transport', 'channel disconnected'))
      }
      ch.streamListeners.clear()
      ch.reassembly.clear()

      // Reject pending claim verification.
      ch.claimReject?.(new ChannelError('transport', 'channel disconnected'))
      ch.claimResolve = undefined
      ch.claimReject = undefined

      ch.closed = true
      this.channels.delete(channelId)
    }

    this.openingChannels.clear()
    this.notifyStateChange()
  }
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')
}
