/**
 * Open-path coordinator: handshake params → TOFU pin → Hub openChannel →
 * Noise finish → relay → generation fence → `'opening'` → verify Ping → pin commit.
 *
 * Extracted from ChannelManager so inbound dispatch and open/verify ordering
 * can evolve independently. See https://github.com/leapmux/leapmux/issues/292.
 */
import type { ChannelPool } from './channelPool'
import type { ChannelRelay } from './channelRelay'
import type { PendingRequest, StreamListener } from './channelRpc'
import type { ChannelSession } from './channelSession'
import type { KeyPinStore } from './keyPinStore'
import type { Session } from './noise'
import type { WorkerKeyBundle } from './workerKeyBundle'
import { MAX_CHUNK_SIZE, MAX_CONFIGURABLE_MESSAGE_SIZE } from '~/generated/contracts/wire'
import { EncryptionMode } from '~/generated/proto/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { formatErrorMessage } from './errors'
import { KeyPinRejectedError } from './keyPinStore'
import { monotonicNow } from './monotonicNow'
import { maxReassembledMessageSize, Reassembler } from './reassembler'

/** Transport slice the open path needs (matches ChannelTransport). */
export interface ChannelOpenTransport {
  getWorkerHandshakeParams: (workerId: string) => Promise<{ keys: WorkerKeyBundle, encryptionMode: EncryptionMode }>
  openChannel: (workerId: string, handshakePayload: Uint8Array) => Promise<{ channelId: string, handshakePayload: Uint8Array, userId: string, maxMessageSize?: number }>
  closeChannel: (channelId: string) => Promise<void>
}

/** Channel record registered at `'opening'` before Ping verify. */
export interface OpeningChannel {
  channelId: string
  workerId: string
  session: Session
  userId: string
  maxMessageSize: number
  maxReassembledMessageSize: number
  pendingRequests: Map<number, PendingRequest>
  streamListeners: Map<number, StreamListener>
  reassembly: Reassembler
  nextRequestId: number
  state: 'opening' | 'verified' | 'closed'
  lastRekeyAt: number
  rekeyNotBefore: number
  rekeyWait: ((accepted: boolean, retryAfterMs: number) => void) | null
  rekeyClear: (() => void) | null
  rekeyAbort: (() => void) | null
  rekeyRequestId: number | null
  rekeyChain: Promise<void>
  /** Worker ML-KEM pub retained for rekey encapsulation; empty in classic mode. */
  workerMlkemPub: Uint8Array
  /** In-flight rekey initiator material; null when no rekey is in progress. */
  rekeyMaterial: { ePriv: Uint8Array, mlkemSS: Uint8Array | null } | null
}

export interface ChannelOpenDeps<T extends OpeningChannel> {
  transport: ChannelOpenTransport
  keyPins: KeyPinStore
  session: ChannelSession
  relay: ChannelRelay
  pool: ChannelPool<T>
  expectedUserId: () => string | undefined
  testPayloadBudget?: number
  testReassembledCeiling?: number
  verifySession: (channel: T) => Promise<void>
  evictGhost: (channelId: string, reason: ChannelError) => void
  notifyStateChange: () => void
}

export class ChannelOpen<T extends OpeningChannel = OpeningChannel> {
  constructor(private readonly deps: ChannelOpenDeps<T>) {}

  /**
   * Open path without pool reuse / single-flight — only called as the factory
   * inside ChannelPool.getOrOpenChannel / dedupeOpen (which already own the lock).
   */
  async openUncached(workerId: string): Promise<string> {
    // Captured before the first await; see closeGeneration.
    const openedGeneration = this.deps.pool.closeGeneration
    // 1. Get Worker's handshake params (keys + live encryption mode) in one RPC.
    const { keys: keyBundle, encryptionMode: mode } = await this.deps.transport.getWorkerHandshakeParams(workerId)

    // 2. Key pinning (TOFU model) — resolve the pin now, record it once the channel
    //    is proven (see commitPin below).
    let commitPin: () => void
    try {
      commitPin = await this.deps.keyPins.resolve(workerId, keyBundle)
    }
    catch (err) {
      if (err instanceof KeyPinRejectedError)
        throw new ChannelError('client', err.message)
      throw err instanceof ChannelError
        ? err
        : new ChannelError('client', err instanceof Error ? err.message : formatErrorMessage(err))
    }

    // 3. Build handshake message 1 based on encryption mode. Completing the
    //    handshake (message 2) is deferred into the try below: it runs only after
    //    the Hub has registered the channel, so a malformed or forged handshake-2
    //    — wrong length, bad AEAD tag, invalid SLH-DSA signature — must roll that
    //    registration back like every later failure.
    const { message1, finish: finishHandshake } = this.deps.session.beginHandshake(mode, keyBundle)

    const result = await this.deps.transport.openChannel(workerId, message1)

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
        await this.deps.transport.closeChannel(result.channelId)
      }
      catch {
        // Best effort: the open is already failing and the Hub expires channels with
        // the credential that opened them, so a failed rollback must not mask the
        // real error.
      }
    }

    try {
      // Adopt negotiated limits before handshake-2 crypto, matching the Go
      // tunnel client (backend/tunnel/channel.go validates max_message_size
      // before handshaker.finish). Missing/out-of-bounds fails closed without
      // paying ML-KEM/SLH-DSA verify cost.
      const limits = this.resolveMessageLimits(result)

      // Reject a hub that did not name the authenticated user, rather than falling
      // back to a locally-asserted identity: the whole point of binding to the
      // Hub-authenticated id is that a stale local one (an account or impersonation
      // switch) can never be asserted. The Go client of this protocol enforces the
      // same invariant at the same boundary (backend/tunnel/channel.go, "hub
      // returned an empty authenticated user id").
      if (!result.userId) {
        throw new ChannelError('transport', 'open channel: hub returned an empty authenticated user id', { disconnected: false })
      }

      // Cross-check the Hub's answer against who this page thinks it is. Taking the
      // identity and DISCARDING it — which is what this did before — means a tab
      // rendered as A whose cookie jar is now B opens a channel the Hub
      // authenticates as B and silently drives B's session with A's UI. The Hub
      // still wins; the open just fails instead of proceeding on a disagreement.
      const expectedUserId = this.deps.expectedUserId()
      if (this.deps.pool.identityMismatch(expectedUserId, result.userId)) {
        throw new ChannelError(
          'transport',
          `open channel: hub authenticated this channel as ${result.userId}, not the expected ${expectedUserId}`,
          { disconnected: false },
        )
      }

      // Complete the Noise handshake. Verification of the Worker's handshake-2
      // message throws on tampering or corruption, and the Hub has already
      // registered the channel, so this sits inside the rollback's coverage — the
      // Go client covers the same step the same way (handshaker.finish runs under
      // the rollback defer in backend/tunnel/channel.go).
      const session = finishHandshake(result.handshakePayload)

      // 4. Ensure shared WebSocket is connected.
      await this.deps.relay.ensureWebSocket()

      // A closeAll that ran while this open was parked on an await has already
      // snapshotted the pool; registering now would slip past it (see
      // closeGeneration). Thrown inside the try so the catch below rolls the
      // Hub-registered channel back.
      if (this.deps.pool.closeGeneration !== openedGeneration) {
        throw new ChannelError('transport', 'open channel: superseded by a concurrent closeAll')
      }

      const channel = {
        channelId: result.channelId,
        workerId,
        session,
        userId: result.userId,
        maxMessageSize: limits.payload,
        maxReassembledMessageSize: limits.reassembled,
        pendingRequests: new Map(),
        streamListeners: new Map(),
        reassembly: new Reassembler(limits.reassembled),
        nextRequestId: 1,
        state: 'opening' as const,
        lastRekeyAt: monotonicNow(),
        rekeyNotBefore: 0,
        rekeyWait: null,
        rekeyClear: null,
        rekeyAbort: null,
        rekeyRequestId: null,
        rekeyChain: Promise.resolve(),
        // Retain the worker's static ML-KEM key so a later in-band rekey can
        // encapsulate a fresh PQ ciphertext without a second
        // getWorkerHandshakeParams round trip. Empty in classic mode; its length
        // is the single source of truth for PQ-ness in the rekey path.
        workerMlkemPub: mode === EncryptionMode.CLASSIC ? new Uint8Array(0) : keyBundle.mlkemPublicKey,
        rekeyMaterial: null,
      } as T

      this.deps.pool.set(result.channelId, channel)

      // 5. Prove the session decrypts both ways before publishing as verified.
      await this.deps.verifySession(channel)

      // Pin only after the session is proven — never pin an unproven key.
      commitPin()
    }
    catch (err) {
      // A failure AFTER verifySession marked the channel verified (say a future
      // commitPin that can fail) would otherwise leave a verified ghost that
      // getOrOpenChannel serves until pastHardCeiling closes it while every RPC on it
      // times out against the Hub registration the rollback below drops. evictGhost
      // is a no-op when verifySession already removed the channel.
      this.deps.evictGhost(
        result.channelId,
        err instanceof ChannelError ? err : new ChannelError('transport', `open channel: ${formatErrorMessage(err)}`),
      )
      await rollback()
      throw err
    }

    // The channel is the caller's now; closeChannel owns the Hub-side teardown.
    registered = false

    this.deps.notifyStateChange()
    return result.channelId
  }

  resolveMessageLimits(result: { maxMessageSize?: number }): { payload: number, reassembled: number } {
    const negotiated = result.maxMessageSize
    if (negotiated != null && negotiated > 0) {
      if (negotiated < MAX_CHUNK_SIZE || negotiated > MAX_CONFIGURABLE_MESSAGE_SIZE) {
        throw new ChannelError(
          'transport',
          `open channel: max_message_size ${negotiated} out of bounds [${MAX_CHUNK_SIZE}, ${MAX_CONFIGURABLE_MESSAGE_SIZE}]`,
        )
      }
      return { payload: negotiated, reassembled: maxReassembledMessageSize(negotiated) }
    }
    if (this.deps.testPayloadBudget != null || this.deps.testReassembledCeiling != null) {
      const payload = this.deps.testPayloadBudget ?? this.deps.testReassembledCeiling!
      const reassembled = this.deps.testReassembledCeiling ?? maxReassembledMessageSize(payload)
      return { payload, reassembled }
    }
    throw new ChannelError('transport', 'open channel: hub returned no max_message_size', { disconnected: false })
  }
}
