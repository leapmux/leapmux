/**
 * Open-path coordinator: handshake params → TOFU pin → Hub openChannel →
 * Noise finish → relay → generation fence → `'opening'` → verify Ping → pin commit.
 * The handshake itself runs in the channel session worker (see
 * channelSessionClient.ts); this path only carries its messages.
 *
 * Extracted from ChannelManager so inbound dispatch and open/verify ordering
 * can evolve independently. See https://github.com/leapmux/leapmux/issues/292.
 */
import type { ChannelOpenMode, ChannelOpenScope, ChannelPool, PooledChannel } from './channelPool'
import type { ChannelRelay } from './channelRelay'
import type { RpcChannel } from './channelRpc'
import type { FinishHandshakeParams, SessionHandshake } from './channelSessionClient'
import type { KeyPinStore, ResolvedKeyPin } from './keyPinStore'
import type { WorkerKeyBundle } from './workerKeyBundle'
import type { EncryptionMode } from '~/generated/proto/leapmux/v1/channel_pb'
import { MAX_CHUNK_SIZE, MAX_CONFIGURABLE_MESSAGE_SIZE } from '~/generated/contracts/wire'
import { ChannelError } from './channelError'
import { FIRST_REQUEST_ID } from './channelSessionProtocol'
import { formatErrorMessage } from './errors'
import { KeyPinRejectedError } from './keyPinStore'
import { maxReassembledMessageSize } from './reassembler'

/** Transport slice the open path needs (matches ChannelTransport). */
export interface ChannelOpenTransport {
  getWorkerHandshakeParams: (workerId: string) => Promise<{ keys: WorkerKeyBundle, encryptionMode: EncryptionMode }>
  openChannel: (workerId: string, handshakePayload: Uint8Array) => Promise<{ channelId: string, handshakePayload: Uint8Array, userId: string, maxMessageSize?: number }>
  closeChannel: (channelId: string) => Promise<void>
}

/**
 * The session worker's half of an open: the handshake, and the close of a
 * session that the open registered but cannot hand out. ChannelSessionClient
 * implements it.
 */
export interface ChannelOpenSession {
  /** Start the session worker ahead of the handshake, so that it loads while the open waits for the Hub. */
  prepare: () => void
  beginHandshake: (workerId: string, mode: EncryptionMode, keys: WorkerKeyBundle) => Promise<SessionHandshake>
  finishHandshake: (handshake: SessionHandshake, params: FinishHandshakeParams) => Promise<void>
  abandonHandshake: (handshake: SessionHandshake) => void
  close: (channelId: string) => void
}

/**
 * The channel record that the open registers at `'opening'`, and that the pool
 * holds through `'verified'` until `'closed'`: the RPC slice, its lifecycle,
 * and what the open negotiated. The Noise session lives in the session worker.
 */
export interface OpeningChannel extends RpcChannel, PooledChannel {
  publicKeyHex: string
  retirement: AbortController
  closeOnHub: () => Promise<void>
  /**
   * Negotiated application payload budget from OpenChannelResponse.max_message_size.
   * Clients never configure this; open refuses a missing/out-of-bounds value
   * (except the test-only ChannelManagerOpts payload/reassembled fallback).
   */
  maxMessageSize: number
  /** The send ceiling of the channel: maxReassembledMessageSize(maxMessageSize), or the test-only testReassembledCeiling. */
  maxReassembledMessageSize: number
}

export interface ChannelOpenDeps<T extends OpeningChannel> {
  transport: ChannelOpenTransport
  keyPins: KeyPinStore
  session: ChannelOpenSession
  relay: Pick<ChannelRelay, 'ensureWebSocket'>
  pool: ChannelPool<T>
  testPayloadBudget?: number
  testReassembledCeiling?: number
  verifySession: (channel: T) => Promise<void>
  evictGhost: (channel: T, reason: ChannelError) => void
  notifyStateChange: () => void
}

export class ChannelOpen<T extends OpeningChannel = OpeningChannel> {
  constructor(private readonly deps: ChannelOpenDeps<T>) {}

  /**
   * Open path without pool reuse / single-flight — only called as the factory
   * inside ChannelPool.getOrOpenChannel / dedupeOpen (which already own the lock).
   */
  async openUncached(workerId: string, scope: ChannelOpenScope, mode: ChannelOpenMode): Promise<string> {
    scope.assertCurrent()
    const retirement = new AbortController()
    const signal = AbortSignal.any([scope.signal, retirement.signal])
    // Load the session worker while the Hub round trips below run.
    this.deps.session.prepare()
    // 1. Get Worker's handshake params (keys + live encryption mode) in one RPC.
    const { keys: keyBundle, encryptionMode } = await this.deps.transport.getWorkerHandshakeParams(workerId)
    scope.assertCurrent()

    // 2. Key pinning (TOFU model) — resolve the pin now, record it once the channel
    //    is proven.
    let pin: ResolvedKeyPin
    try {
      pin = await this.deps.keyPins.resolve(workerId, keyBundle, {
        accountId: scope.accountId,
        signal,
        ...(scope.identity === undefined
          ? {}
          : { identitySignal: scope.identity.signal }),
        review: mode === 'review',
        assertCurrent: scope.assertCurrent,
      })
    }
    catch (err) {
      signal.throwIfAborted()
      if (mode === 'review' && err instanceof KeyPinRejectedError)
        throw err
      throw err instanceof ChannelError
        ? err
        : new ChannelError('client', err instanceof Error
            ? err.message
            : formatErrorMessage(err))
    }

    // 3. Build handshake message 1 based on encryption mode. Completing the
    //    handshake (message 2) is deferred into the try below: it runs only after
    //    the Hub has registered the channel, so a malformed or forged handshake-2
    //    — wrong length, bad AEAD tag, invalid SLH-DSA signature — must roll that
    //    registration back like every later failure.
    scope.assertCurrent()
    const handshake = await this.deps.session.beginHandshake(workerId, encryptionMode, keyBundle)
    // Every exit from here either hands the handshake to finishHandshake, which
    // spends it, or abandons it, so that its state never outlives the open.
    let handshakeHeld = true
    const abandonHandshake = () => {
      if (!handshakeHeld)
        return
      handshakeHeld = false
      this.deps.session.abandonHandshake(handshake)
    }
    try {
      scope.assertCurrent()
      const result = await this.deps.transport.openChannel(workerId, handshake.message1)

      // From here on the Hub has REGISTERED a channel and the Worker holds a live Noise
      // session, so every failure exit must tell the Hub to drop it. Without that, a
      // retry loop against a bad worker (a flaky relay failing the ping) strands a
      // channel per attempt -- on the Hub's index and as a Worker session plus its
      // goroutine and per-channel caps -- until the credential is revoked or the
      // process restarts. The Go client of this protocol rolls back at exactly this
      // boundary (backend/tunnel/channel.go's `rollback` flag +
      // rollbackRegisteredChannel).
      let closing: Promise<void> | undefined
      const closeOnHub = () => {
        closing ??= Promise.resolve()
          .then(() => this.deps.transport.closeChannel(result.channelId))
          .catch(() => {
            // The Hub also expires channels with the credential that opened them.
          })
        return closing
      }

      let channel: T | undefined
      try {
        scope.assertCurrent()
        // Adopt negotiated limits before handshake-2 crypto, matching the Go
        // tunnel client (backend/tunnel/channel.go validates max_message_size
        // before HandshakeState.Finish). Missing/out-of-bounds fails closed without
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
        const expectedUserId = scope.identity?.userId ?? undefined
        if (this.deps.pool.identityMismatch(expectedUserId, result.userId)) {
          throw new ChannelError(
            'transport',
            `open channel: hub authenticated this channel as ${result.userId}, not the expected ${expectedUserId}`,
            { disconnected: false },
          )
        }

        // Complete the Noise handshake in the session worker, which registers the
        // channel's session there. Verification of the Worker's handshake-2
        // message throws on tampering or corruption, and the Hub has already
        // registered the channel, so this sits inside the rollback's coverage — the
        // Go client covers the same step the same way (HandshakeState.Finish runs under
        // the rollback defer in backend/tunnel/channel.go). The finish spends the
        // handshake whatever its outcome, and a failed one registers nothing.
        handshakeHeld = false
        await this.deps.session.finishHandshake(handshake, {
          channelId: result.channelId,
          payload: result.handshakePayload,
          maxReassembledMessageSize: limits.reassembled,
        })

        // 4. Ensure the shared WebSocket is connected.
        scope.assertCurrent()
        await this.deps.relay.ensureWebSocket()
        scope.assertCurrent()

        channel = ({
          channelId: result.channelId,
          workerId,
          userId: result.userId,
          scope,
          retirement,
          closeOnHub,
          publicKeyHex: pin.publicKeyHex,
          maxMessageSize: limits.payload,
          maxReassembledMessageSize: limits.reassembled,
          pendingRequests: new Map(),
          streamListeners: new Map(),
          nextRequestId: FIRST_REQUEST_ID,
          state: 'opening' as const,
        } satisfies OpeningChannel) as T

        this.deps.pool.set(result.channelId, channel)

        const opening = channel
        const assertOpening = () => {
          scope.assertCurrent()
          signal.throwIfAborted()
          if (opening.state !== 'opening' || this.deps.pool.get(opening.channelId) !== opening)
            throw new ChannelError('transport', 'open channel: opening record retired')
        }

        // 5. Prove the session and commit its pin before offering it to callers.
        await this.deps.verifySession(opening)
        assertOpening()
        await pin.commit()
        assertOpening()
        opening.state = 'verified'
      }
      catch (err) {
        abandonHandshake()
        if (channel === undefined) {
          // Handshake completion can register a session before it enters the pool.
          this.deps.session.close(result.channelId)
        }
        else if (this.deps.pool.get(result.channelId) === channel) {
          this.deps.evictGhost(
            channel,
            err instanceof ChannelError
              ? err
              : new ChannelError('transport', `open channel: ${formatErrorMessage(err)}`),
          )
        }
        await closeOnHub()
        throw err
      }

      this.deps.notifyStateChange()
      return result.channelId
    }
    finally {
      abandonHandshake()
    }
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
      return {
        payload: negotiated,
        reassembled: maxReassembledMessageSize(negotiated),
      }
    }
    if (this.deps.testPayloadBudget != null || this.deps.testReassembledCeiling != null) {
      const payload = this.deps.testPayloadBudget ?? this.deps.testReassembledCeiling!
      const reassembled = this.deps.testReassembledCeiling ?? maxReassembledMessageSize(payload)
      return { payload, reassembled }
    }
    throw new ChannelError('transport', 'open channel: hub returned no max_message_size', { disconnected: false })
  }
}
