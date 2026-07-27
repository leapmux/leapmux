/**
 * Per-channel Noise session for the E2EE channel: handshake (classic /
 * hybrid), encrypt/chunk send, decrypt, in-band rekey policy (age / soft-nonce /
 * hard ceiling / reject backoff), and RekeyRequest ↔ Ack/Reject correlation.
 * Extracted from ChannelManager so the crypto state machine is unit-testable
 * without a full open harness.
 *
 * See https://github.com/leapmux/leapmux/issues/292.
 */
import type { Session } from './noise'
import type { WorkerKeyBundle } from './workerKeyBundle'
import { create, toBinary } from '@bufbuild/protobuf'
import {
  ChannelMessageFlags,
  ChannelMessageSchema,
  EncryptionMode,
  InnerMessageSchema,
  RekeyRequestSchema,
} from '~/generated/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { frameBytes } from './channelFraming'
import { formatErrorMessage } from './errors'
import { createLogger } from './logger'
import { monotonicNow } from './monotonicNow'
import { initiatorHandshake1 as classicHandshake1, initiatorHandshake2 as classicHandshake2, DH_LEN } from './noise'
import {
  deriveRekeySecrets,
  encapsulateRekeyPQ,
  generateRekeyEphemeral,
  initiatorHandshake1,
  initiatorHandshake2,
} from './noise-hybrid'
import { MAX_CHUNK_SIZE } from './reassembler'

const log = createLogger('channel')

/**
 * How long a Noise transport key may live before the initiator should request
 * an in-band rekey. Must match channelwire.SessionKeyMaxAge / the fixture.
 */
export const SESSION_KEY_MAX_AGE_MS = 60 * 60 * 1000 // 1 hour

/**
 * Earliest another age-only rekey may be accepted after a successful one.
 * Ten minutes of headroom under SESSION_KEY_MAX_AGE_MS; soft nonce bypasses.
 * Must match channelwire.MinRekeyInterval / the fixture.
 */
export const MIN_REKEY_INTERVAL_MS = 50 * 60 * 1000 // 50 minutes

/**
 * Absolute age past which the channel must close and re-handshake instead of
 * serving under the old key. Matches channelwire.SessionKeyHardCeiling.
 */
export const SESSION_KEY_HARD_CEILING_MS = SESSION_KEY_MAX_AGE_MS + 10 * 60 * 1000 // 70 minutes

/** Open-time Ping Ack/Reject budget, matching Go sessionVerifyTimeout. */
const REKEY_TIMEOUT_MS = 10_000

/** Fallback when RekeyReject.retry_after_ms is unset (legacy peers). Matches channelwire.DefaultRejectBackoff. */
const DEFAULT_REJECT_BACKOFF_MS = 60_000

/**
 * Same shape as transport / TOFU key material — see workerKeyBundle.ts.
 */
export type SessionKeyBundle = WorkerKeyBundle

/**
 * Initiator handshake message 1 plus a deferred finish that consumes the
 * Worker's handshake-2 payload. Finish is deferred so ChannelManager can roll
 * the Hub registration back if handshake-2 crypto fails after openChannel.
 */
export interface PendingHandshake {
  message1: Uint8Array
  finish: (handshakePayload: Uint8Array) => Session
}

/**
 * The slice of an ActiveChannel the session helpers drive: Noise keys, age
 * policy fields, and the outbound id allocator used for RekeyRequest.
 */
export interface SessionChannel {
  channelId: string
  session: Session
  maxReassembledMessageSize: number
  nextRequestId: number
  state: 'opening' | 'verified' | 'closed'
  lastRekeyAt: number
  rekeyNotBefore: number
  rekeyWait: ((accepted: boolean, retryAfterMs: number) => void) | null
  /** Clears rekey timer/waiter without rejecting (send-failure path). */
  rekeyClear: (() => void) | null
  /** Clears and rejects an in-flight ensureRekeyed (closeChannel / teardown). */
  rekeyAbort: (() => void) | null
  rekeyRequestId: number | null
  rekeyChain: Promise<void>
  /**
   * Worker's static ML-KEM-1024 encapsulation key, retained from
   * getWorkerHandshakeParams so an in-band rekey can encapsulate a fresh PQ
   * ciphertext without a second round trip. Empty on classic-mode channels;
   * its length is the single source of truth for "is this a PQ channel" in the
   * rekey path (encapsulateRekeyPQ short-circuits on length === 0).
   */
  workerMlkemPub: Uint8Array
  /**
   * Initiator-side fresh key-agreement material for one in-flight rekey: the
   * local ephemeral private key and the ML-KEM shared secret. Set when the
   * RekeyRequest is sent, consumed when the matching Ack arrives. Null outside
   * an in-flight rekey.
   */
  rekeyMaterial: { ePriv: Uint8Array, mlkemSS: Uint8Array | null } | null
}

export interface ChannelSessionDeps {
  /** Write one already-framed ChannelMessage buffer to the live WebSocket. */
  sendToWire: (buf: Uint8Array) => void
  /** Close a channel (hard-ceiling / rekey-timeout paths). */
  closeChannel: (channelId: string) => Promise<void>
  /** Session-level send failure that should retire the pooled channel. */
  onSendFailure: (ch: SessionChannel, err: unknown) => void
}

/** Injectable Noise handshake implementations (production defaults; tests override). */
export interface ChannelSessionOpts {
  handshake1?: typeof initiatorHandshake1
  handshake2?: typeof initiatorHandshake2
  classicHandshake1?: typeof classicHandshake1
  classicHandshake2?: typeof classicHandshake2
}

/**
 * Owns Noise handshake, encrypt/chunk send, decrypt, and in-band rekey policy
 * for one ChannelManager.
 */
export class ChannelSession {
  private deps: ChannelSessionDeps
  private handshake1: typeof initiatorHandshake1
  private handshake2: typeof initiatorHandshake2
  private classicHS1: typeof classicHandshake1
  private classicHS2: typeof classicHandshake2

  constructor(deps: ChannelSessionDeps, opts?: ChannelSessionOpts) {
    this.deps = deps
    this.handshake1 = opts?.handshake1 ?? initiatorHandshake1
    this.handshake2 = opts?.handshake2 ?? initiatorHandshake2
    this.classicHS1 = opts?.classicHandshake1 ?? classicHandshake1
    this.classicHS2 = opts?.classicHandshake2 ?? classicHandshake2
  }

  /**
   * Build handshake message 1 for the given encryption mode. Completing the
   * handshake (message 2) is returned as `finish` so the coordinator can run
   * it only after the Hub has registered the channel — a malformed or forged
   * handshake-2 must roll that registration back like every later failure.
   */
  beginHandshake(mode: EncryptionMode, keys: SessionKeyBundle): PendingHandshake {
    if (mode === EncryptionMode.CLASSIC) {
      const hs = this.classicHS1(keys.x25519PublicKey)
      return {
        message1: hs.message1,
        finish: payload => this.classicHS2(hs.handshakeState, payload),
      }
    }
    const hs = this.handshake1(keys.x25519PublicKey, keys.mlkemPublicKey)
    return {
      message1: hs.message1,
      finish: payload => this.handshake2(hs.handshakeState, payload, keys.slhdsaPublicKey),
    }
  }

  /**
   * Decrypt one inbound ciphertext under the channel's receive cipher.
   * Callers MUST decrypt before dropping a frame for a bad correlation id or
   * out-of-spec flags: Noise nonces are implicit and sequential, so skipping a
   * ciphertext desyncs the receive nonce and poisons the rest of the channel.
   */
  decrypt(ch: SessionChannel, ciphertext: Uint8Array): Uint8Array {
    return ch.session.receive.decrypt(ciphertext)
  }

  /** Whether initiator policy says we should start an in-band rekey. */
  shouldInitiateRekey(ch: SessionChannel, now = monotonicNow()): boolean {
    const soft = ch.session.send.needsRekey() || ch.session.receive.needsRekey()
    if (soft)
      return true
    if (ch.rekeyNotBefore > now)
      return false
    return now - ch.lastRekeyAt >= SESSION_KEY_MAX_AGE_MS
  }

  /** Absolute age past which the channel must close and re-handshake. */
  pastHardCeiling(ch: SessionChannel, now = monotonicNow()): boolean {
    return now - ch.lastRekeyAt >= SESSION_KEY_HARD_CEILING_MS
  }

  /** True when outbound encrypt must wait on ensureRekeyed (age/soft-nonce or hard ceiling). */
  needsRekeyGate(ch: SessionChannel, now = monotonicNow()): boolean {
    return this.pastHardCeiling(ch, now) || this.shouldInitiateRekey(ch, now)
  }

  /**
   * Encrypt and send plaintext, splitting into chunks if needed.
   * All chunks share the same correlationId. Intermediate chunks have
   * flags=MORE; the final chunk has flags=UNSPECIFIED.
   */
  sendEncryptedMessage(ch: SessionChannel, plaintext: Uint8Array, requestId: number): void {
    if (plaintext.length > ch.maxReassembledMessageSize) {
      throw new ChannelError('client', `message too large: ${plaintext.length} > ${ch.maxReassembledMessageSize}`)
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

  private sendChannelMessage(
    ch: SessionChannel,
    ciphertext: Uint8Array,
    requestId: number,
    flags: ChannelMessageFlags = ChannelMessageFlags.UNSPECIFIED,
  ): void {
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
    this.deps.sendToWire(frameBytes(data))
  }

  /**
   * Initiate in-band rekey when age or soft-nonce policy requires it. Holds a
   * per-channel chain so concurrent callers share one Request→Ack/Reject RTT
   * and no app frame is encrypted until keys settle (or Reject leaves them).
   */
  ensureRekeyed(ch: SessionChannel): Promise<void> {
    const run = ch.rekeyChain.then(() => this.ensureRekeyedLocked(ch))
    ch.rekeyChain = run.then(() => undefined, () => undefined)
    return run
  }

  private async ensureRekeyedLocked(ch: SessionChannel): Promise<void> {
    if (ch.state === 'closed')
      return
    if (this.pastHardCeiling(ch)) {
      await this.deps.closeChannel(ch.channelId)
      throw new ChannelError('transport', 'session key past hard ceiling')
    }
    if (!this.shouldInitiateRekey(ch))
      return

    const requestId = ch.nextRequestId++
    const outcome = new Promise<{ accepted: boolean, retryAfterMs: number }>((resolve, reject) => {
      /* eslint-disable ts/no-use-before-define -- clearRekeySlot / timer mutually reference */
      const timer = setTimeout(() => {
        if (ch.rekeyWait) {
          clearRekeySlot()
          void this.deps.closeChannel(ch.channelId)
          reject(new ChannelError('transport', 'rekey timeout'))
        }
      }, REKEY_TIMEOUT_MS)
      const clearRekeySlot = () => {
        clearTimeout(timer)
        ch.rekeyWait = null
        ch.rekeyClear = null
        ch.rekeyAbort = null
        ch.rekeyRequestId = null
        // Wipe the in-flight fresh key-agreement material. The success path
        // zeroes it in handleRekeyOutcome before resolving; the timeout and
        // channel-close paths reach here instead, and must not leave the fresh
        // X25519 ephemeral / ML-KEM shared secret lingering in the heap.
        const pending = ch.rekeyMaterial
        ch.rekeyMaterial = null
        if (pending !== null) {
          pending.ePriv.fill(0)
          if (pending.mlkemSS !== null)
            pending.mlkemSS.fill(0)
        }
      }
      /* eslint-enable ts/no-use-before-define */
      ch.rekeyClear = clearRekeySlot
      ch.rekeyAbort = () => {
        clearRekeySlot()
        reject(new ChannelError('transport', 'channel closed'))
      }
      ch.rekeyWait = (accepted, retryAfterMs) => {
        clearRekeySlot()
        resolve({ accepted, retryAfterMs })
      }
    })
    ch.rekeyRequestId = requestId

    // Generate the initiator's fresh ephemeral and (for PQ channels) encapsulate
    // a fresh ML-KEM shared secret under the worker's static key, build the
    // RekeyRequest, and put it on the wire. All three steps run inside one try
    // so a throw (a malformed retained workerMlkemPub making encapsulate fail,
    // or a send failure) tears down the waiter/timer just installed above and
    // wipes the fresh material — otherwise the 10s timer would fire on a
    // channel whose rekey never reached the wire and the ephemeral would leak.
    let eph: { privateKey: Uint8Array, publicKey: Uint8Array } | null = null
    let mlkemSS: Uint8Array | null = null
    try {
      eph = generateRekeyEphemeral()
      const encapsulated = encapsulateRekeyPQ(ch.workerMlkemPub)
      mlkemSS = encapsulated.sharedSecret
      ch.rekeyMaterial = { ePriv: eph.privateKey, mlkemSS }

      const envelope = create(InnerMessageSchema, {
        kind: {
          case: 'rekeyRequest',
          value: create(RekeyRequestSchema, {
            dhPub: eph.publicKey,
            mlkemCt: encapsulated.cipherText ?? new Uint8Array(0),
          }),
        },
      })
      this.sendEncryptedMessage(ch, toBinary(InnerMessageSchema, envelope), requestId)
    }
    catch (err) {
      // Drop the waiter/timer without rejecting `outcome` — we throw so callers
      // see the failure, not a synthetic "channel closed". clearRekeySlot also
      // wipes rekeyMaterial; the locals are zeroed here in case generation
      // failed before the material was stashed.
      ch.rekeyClear?.()
      if (eph !== null)
        eph.privateKey.fill(0)
      if (mlkemSS !== null)
        mlkemSS.fill(0)
      this.deps.onSendFailure(ch, err)
      throw err instanceof Error ? err : new ChannelError('client', formatErrorMessage(err))
    }

    const { accepted, retryAfterMs } = await outcome
    if (accepted) {
      ch.lastRekeyAt = monotonicNow()
      ch.rekeyNotBefore = 0
    }
    else {
      const backoff = retryAfterMs > 0 ? retryAfterMs : DEFAULT_REJECT_BACKOFF_MS
      ch.rekeyNotBefore = monotonicNow() + backoff
      if (this.pastHardCeiling(ch)) {
        await this.deps.closeChannel(ch.channelId)
        throw new ChannelError('transport', 'session key past hard ceiling after rekey reject')
      }
    }
  }

  /**
   * Abort an in-flight rekey waiter (closeChannel / teardown). Clears the
   * timeout and rejects ensureRekeyed callers immediately instead of waiting
   * for REKEY_TIMEOUT_MS.
   */
  abortRekey(ch: SessionChannel): void {
    ch.rekeyAbort?.()
  }

  handleRekeyOutcome(ch: SessionChannel, accepted: boolean, retryAfterMs = 0, correlationId?: number, responderPub?: Uint8Array): void {
    if (!ch.rekeyWait) {
      log.warn('rekey outcome with no in-flight request', { channel_id: ch.channelId, accepted })
      return
    }
    // Ignore Ack/Reject that does not match the in-flight Request id — a
    // mismatched/spurious outcome must not rotate or settle the waiter.
    if (ch.rekeyRequestId !== null && correlationId !== undefined && correlationId !== ch.rekeyRequestId) {
      log.warn('ignoring rekey outcome with mismatched correlation id', {
        channel_id: ch.channelId,
        expected: ch.rekeyRequestId,
        got: correlationId,
        accepted,
      })
      return
    }
    const rm = ch.rekeyMaterial
    ch.rekeyMaterial = null
    // Every exit path below must wipe the fresh key-agreement material — the
    // forward-secrecy guarantee of #321 depends on the ephemeral not lingering
    // in the heap past the rekey it was generated for. clearRekeySlot (which
    // fires on timeout / channel close) reads ch.rekeyMaterial, which we just
    // nulled, so it cannot reach these bytes; the local `rm` is the only live
    // reference. Mirrors Go's resolveRekey, which zeroes unconditionally on
    // every resolution (accept / reject / terminal failure).
    const wipe = (): void => {
      if (rm === null)
        return
      rm.ePriv.fill(0)
      if (rm.mlkemSS !== null)
        rm.mlkemSS.fill(0)
    }
    if (accepted) {
      // Combine the responder's Ack ephemeral with the stashed local ephemeral
      // to complete the fresh-DH agreement, then rotate both directions. Each
      // direction needs its own secret copies: rekeyWithSecret zeroes its
      // inputs, and both must mix identical entropy.
      if (rm === null || responderPub === undefined || responderPub.length !== DH_LEN) {
        log.error('rekey ack missing local material or valid responder ephemeral; closing channel', {
          channel_id: ch.channelId,
          responder_pub_len: responderPub?.length ?? -1,
        })
        wipe()
        // Funnel teardown through rekeyAbort (the close/teardown path) rather
        // than hand-nulling rekeyWait: rekeyAbort → clearRekeySlot clears the
        // 10s timer + nulls rekeyWait/rekeyClear/rekeyAbort/rekeyRequestId AND
        // rejects the outcome promise ensureRekeyed is awaiting. Hand-nulling
        // rekeyWait left the timer armed and the slot half-cleared, relying on
        // the closeChannel → abortRekey call below to finish the job — an
        // invisible cross-method contract no test pinned. clearRekeySlot is
        // idempotent, so the abortRekey that closeChannel fires next is a no-op.
        ch.rekeyAbort?.()
        void this.deps.closeChannel(ch.channelId)
        return
      }
      const { dhSecret, pqSecret } = deriveRekeySecrets(rm.ePriv, responderPub, rm.mlkemSS)
      const dhForRecv = dhSecret.slice()
      const pqForRecv = pqSecret !== null ? pqSecret.slice() : null
      // retainPrev=false for send (it keeps no grace window — the replaced key
      // is zeroed structurally, no follow-up clearPrev to remember); true for
      // receive (retains prev so in-flight old-key frames still decrypt).
      ch.session.send.rekeyWithSecret(dhSecret, pqSecret, false)
      ch.session.receive.rekeyWithSecret(dhForRecv, pqForRecv, true)
      wipe()
    }
    else {
      // Reject: keys are unchanged, but the fresh ephemeral + ML-KEM shared
      // secret generated for this attempt must still be wiped — otherwise they
      // linger until GC, weakening the forward-secrecy property on every Reject.
      wipe()
    }
    const wait = ch.rekeyWait
    wait(accepted, retryAfterMs)
  }
}
