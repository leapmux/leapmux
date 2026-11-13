/**
 * Per-channel Noise session for the E2EE channel, in the channel session worker:
 * handshake (classic / hybrid), encrypt/chunk send, decrypt, in-band rekey policy
 * (age / soft-nonce / hard ceiling / reject backoff), and RekeyRequest ↔
 * Ack/Reject correlation. The session endpoint (channelSessionEndpoint.ts) drives
 * one ChannelSession for every channel of the tab; the main thread never holds a
 * cipher key.
 *
 * See https://github.com/leapmux/leapmux/issues/292.
 */
import type { HandshakeState, Session } from './noise'
import type { HybridHandshakeState } from './noise-hybrid'
import type { WorkerKeyBundle } from './workerKeyBundle'
import { create, toBinary } from '@bufbuild/protobuf'
import {
  DEFAULT_REJECT_BACKOFF_MS,
  MAX_CHUNK_SIZE,
  SESSION_KEY_HARD_CEILING_MS,
  SESSION_KEY_MAX_AGE_MS,
  SESSION_VERIFY_TIMEOUT_MS,
  X25519_PUBLIC_KEY_SIZE,
} from '~/generated/contracts/wire'
import {
  ChannelMessageFlags,
  ChannelMessageSchema,
  EncryptionMode,
  InnerMessageSchema,
  RekeyRequestSchema,
} from '~/generated/proto/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { frameBytes } from './channelFraming'
import { formatErrorMessage } from './errors'
import { createLogger } from './logger'
import { monotonicNow } from './monotonicNow'
import { initiatorHandshake1 as classicHandshake1, initiatorHandshake2 as classicHandshake2, clearClassicalHandshakeState } from './noise'
import {
  clearHandshakeState,
  deriveRekeySecrets,
  encapsulateRekeyPQ,
  generateRekeyEphemeral,
  initiatorHandshake1,
  initiatorHandshake2,
} from './noise-hybrid'
import { Reassembler } from './reassembler'
import { verifySlhDsaHere } from './slhDsaVerifyHere'

const log = createLogger('channel')

/** How long a RekeyRequest waits for its Ack or Reject: the open-time verify round trip budget, the Go side's channelwire.SessionVerifyTimeout. */
const REKEY_TIMEOUT_MS = SESSION_VERIFY_TIMEOUT_MS

/**
 * The first correlation id of a RekeyRequest. The worker allocates these ids
 * itself, from a range of their own: the main thread allocates request ids
 * synchronously from its own counter, and the worker cannot share that counter.
 * At the saturated rate of about 640 ids per second, the main thread's counter
 * reaches 2^52 in about 220,000 years. The Go worker echoes any uint64 id, and
 * the Hub treats ids as opaque keys. Every id stays below 2^53, so it stays an
 * exact JavaScript number.
 */
export const FIRST_REKEY_CORRELATION_ID = 2 ** 52

/** The session of a channel that the worker no longer has. */
export function sessionGoneError(): ChannelError {
  return new ChannelError('transport', 'channel session is gone')
}

/**
 * Same shape as transport / TOFU key material — see workerKeyBundle.ts.
 */
export type SessionKeyBundle = WorkerKeyBundle

/**
 * Initiator handshake message 1, a deferred finish that consumes the Worker's
 * handshake-2 payload, and an abandon for a handshake that will not finish.
 * Finish is deferred so the main thread can roll the Hub registration back if
 * handshake-2 crypto fails after openChannel.
 *
 * Every way out zeroes the handshake state: `finish` zeroes it whether it
 * succeeds or throws, and `abandon` zeroes it at once.
 */
export interface PendingHandshake {
  message1: Uint8Array
  /** Rejects on a malformed or forged handshake-2, and on a handshake that already finished or was abandoned. */
  finish: (handshakePayload: Uint8Array) => Promise<Session>
  /** Zero the handshake state. Idempotent. */
  abandon: () => void
}

/** A message that waits in the held lane of its channel (see ChannelSession.send). */
export interface HeldSend {
  plaintext: Uint8Array
  correlationId: number
  startsRequest: boolean
}

/**
 * The worker's record of one channel: the Noise keys, the age policy fields,
 * the reassembly buffers, the liveness of the correlation ids, and the held
 * lane of sends that wait for a rekey.
 */
export interface SessionChannel {
  channelId: string
  session: Session
  /** The largest inbound message that the session reassembles: maxReassembledMessageSize(maxMessageSize). */
  maxReassembledMessageSize: number
  /** The inbound chunked messages that this channel still reassembles. */
  reassembly: Reassembler
  /**
   * The correlation ids of the requests and the streams that the main thread
   * still waits on: a `send` that starts a request adds its id, and `release`
   * removes it. A one-way mirror of the main thread's registries, which keeps an
   * inbound chunked message with no consumer from pinning memory.
   */
  liveIds: Set<number>
  /** The largest correlation id that a starting send carried. 0 before the first. */
  highestStartedId: number
  /** The correlation id of the next RekeyRequest (see FIRST_REKEY_CORRELATION_ID). */
  nextRekeyId: number
  /** The sends that wait for a rekey, oldest first. */
  held: HeldSend[]
  /** True while drainHeld sends the held lane. */
  draining: boolean
  /**
   * The ids of the held starting sends that the drain dropped, because the main
   * thread released them before they went out. The drain drops their later held
   * sends too, such as a cancel.
   */
  abandonedIds: Set<number>
  /** Set once the session ended: its keys are zero, and it sends and decrypts nothing more. */
  ended: boolean
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
  /** Clears rekey timer/waiter without rejecting (send-failure path). */
  rekeyClear: (() => void) | null
  /** Clears and rejects an in-flight ensureRekeyed (dispose / teardown). */
  rekeyAbort: (() => void) | null
  /** Correlation id of the in-flight RekeyRequest (so Ack/Reject are not dropped as unknown). */
  rekeyRequestId: number | null
  /** Chains concurrent ensureRekeyed callers on this channel. */
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

/** What a fresh SessionChannel needs; createSessionChannel defaults the rest. */
export interface SessionChannelInit {
  channelId: string
  session: Session
  maxReassembledMessageSize: number
  workerMlkemPub: Uint8Array
}

/** A fresh SessionChannel, whose key age starts now. */
export function createSessionChannel(init: SessionChannelInit): SessionChannel {
  return {
    channelId: init.channelId,
    session: init.session,
    maxReassembledMessageSize: init.maxReassembledMessageSize,
    reassembly: new Reassembler(init.maxReassembledMessageSize),
    liveIds: new Set(),
    highestStartedId: 0,
    nextRekeyId: FIRST_REKEY_CORRELATION_ID,
    held: [],
    draining: false,
    abandonedIds: new Set(),
    ended: false,
    lastRekeyAt: monotonicNow(),
    rekeyNotBefore: 0,
    rekeyWait: null,
    rekeyClear: null,
    rekeyAbort: null,
    rekeyRequestId: null,
    rekeyChain: Promise.resolve(),
    workerMlkemPub: init.workerMlkemPub,
    rekeyMaterial: null,
  }
}

export interface ChannelSessionDeps {
  /** Put one framed ChannelMessage on the wire, after every frame posted before it. */
  postFrame: (channelId: string, frame: Uint8Array) => void
  /**
   * End the session of `ch` with `error`: dispose it, forget it, and tell the
   * main thread, which closes the channel. Does nothing for a session that ended.
   */
  endSession: (ch: SessionChannel, error: ChannelError) => void
}

/** Injectable Noise handshake implementations (production defaults; tests override). */
export interface ChannelSessionOpts {
  handshake1?: typeof initiatorHandshake1
  handshake2?: typeof initiatorHandshake2
  classicHandshake1?: typeof classicHandshake1
  classicHandshake2?: typeof classicHandshake2
}

/** The ChannelError that ends a session after `err` failed an encrypt or a send. */
function sendFailureError(err: unknown): ChannelError {
  return err instanceof ChannelError
    ? err
    : new ChannelError('transport', formatErrorMessage(err))
}

/**
 * A PendingHandshake whose finish runs `complete`, and whose every exit runs
 * `clear` exactly once.
 */
function pendingHandshake(
  message1: Uint8Array,
  complete: (handshakePayload: Uint8Array) => Session | Promise<Session>,
  clear: () => void,
): PendingHandshake {
  let cleared = false
  const clearOnce = (): void => {
    if (cleared)
      return
    cleared = true
    clear()
  }
  return {
    message1,
    finish: async (handshakePayload) => {
      // A zeroed state would run the handshake on zero keys.
      if (cleared)
        throw new Error('noise: the handshake already finished or was abandoned')
      try {
        return await complete(handshakePayload)
      }
      finally {
        clearOnce()
      }
    },
    abandon: clearOnce,
  }
}

/**
 * Owns the Noise handshake, encrypt/chunk send, decrypt, and in-band rekey
 * policy for every channel of the session worker.
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
   * handshake (message 2) is returned as `finish` so the main thread can run
   * it only after the Hub has registered the channel — a malformed or forged
   * handshake-2 must roll that registration back like every later failure.
   *
   * The hybrid finish verifies the SLH-DSA signature on this thread. The
   * session runs in a Web Worker, so the verification holds up no page work.
   */
  beginHandshake(mode: EncryptionMode, keys: SessionKeyBundle): PendingHandshake {
    if (mode === EncryptionMode.CLASSIC) {
      const hs = this.classicHS1(keys.x25519PublicKey)
      const state: HandshakeState = hs.handshakeState
      return pendingHandshake(
        hs.message1,
        payload => this.classicHS2(state, payload),
        () => clearClassicalHandshakeState(state),
      )
    }
    const hs = this.handshake1(keys.x25519PublicKey, keys.mlkemPublicKey)
    const state: HybridHandshakeState = hs.handshakeState
    return pendingHandshake(
      hs.message1,
      payload => this.handshake2(state, payload, keys.slhdsaPublicKey, verifySlhDsaHere),
      () => clearHandshakeState(state),
    )
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
   * Encrypt one message and post its frames, splitting it into chunks if needed.
   * All chunks share the same correlationId. Intermediate chunks have
   * flags=MORE; the final chunk has flags=UNSPECIFIED.
   *
   * The frames of a channel leave in call order. A stream sends its follow-ups
   * and its cancel under its own correlation ID, and the Worker joins every frame
   * of one ID up to the first frame without MORE, so a frame that went out between
   * the chunks of another message would end that message early.
   *
   * A message goes out at once, unless a rekey is due or in flight, or earlier
   * messages still wait. It then waits in the held lane, in arrival order, and
   * goes out once the rekey settles (see drainHeld). A failure to encrypt ends
   * the session, which fails every request of the channel on the main thread.
   */
  send(ch: SessionChannel, plaintext: Uint8Array, correlationId: number, startsRequest: boolean): void {
    if (ch.ended)
      return
    if (startsRequest) {
      ch.liveIds.add(correlationId)
      if (correlationId > ch.highestStartedId)
        ch.highestStartedId = correlationId
    }
    if (ch.held.length > 0 || ch.draining || this.needsRekeyGate(ch)) {
      ch.held.push({ plaintext, correlationId, startsRequest })
      void this.drainHeld(ch)
      return
    }
    try {
      this.encryptAndSend(ch, plaintext, correlationId)
    }
    catch (err) {
      this.deps.endSession(ch, sendFailureError(err))
    }
  }

  /**
   * The main thread stopped waiting on `correlationId`: retire the id, and drop
   * its reassembly buffer. A tombstone stays, so the rest of a message that
   * broke a limit is still dropped (see Reassembler.release).
   */
  release(ch: SessionChannel, correlationId: number): void {
    ch.liveIds.delete(correlationId)
    ch.reassembly.release(correlationId)
  }

  /**
   * Make the session ready to serve a request: end it past the hard ceiling, or
   * rekey it when a rekey is due. Rejects when the session ended, so that the
   * pool opens a fresh channel.
   */
  async ensureReady(ch: SessionChannel): Promise<void> {
    if (ch.ended)
      throw sessionGoneError()
    if (this.pastHardCeiling(ch)) {
      const err = new ChannelError('transport', 'session key past hard ceiling', { disconnected: false })
      this.deps.endSession(ch, err)
      throw err
    }
    if (this.shouldInitiateRekey(ch))
      await this.ensureRekeyed(ch)
    if (ch.ended)
      throw sessionGoneError()
  }

  /**
   * Initiate in-band rekey when age or soft-nonce policy requires it. Holds a
   * per-channel chain so concurrent callers share one Request→Ack/Reject RTT.
   * Every way that this rejects ends the session first.
   */
  ensureRekeyed(ch: SessionChannel): Promise<void> {
    const run = ch.rekeyChain.then(() => this.ensureRekeyedLocked(ch))
    ch.rekeyChain = run.then(() => undefined, () => undefined)
    return run
  }

  /**
   * Abort an in-flight rekey waiter (dispose / teardown). Clears the timeout and
   * rejects ensureRekeyed callers immediately instead of waiting for
   * REKEY_TIMEOUT_MS.
   */
  abortRekey(ch: SessionChannel): void {
    ch.rekeyAbort?.()
  }

  /**
   * End the session for good: abort the rekey, drop every held send and buffer,
   * and zero both cipher states. Idempotent.
   */
  dispose(ch: SessionChannel): void {
    if (ch.ended)
      return
    ch.ended = true
    this.abortRekey(ch)
    ch.held.length = 0
    ch.abandonedIds.clear()
    ch.liveIds.clear()
    ch.reassembly.clear()
    ch.session.send.destroy()
    ch.session.receive.destroy()
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
    // fires on timeout / dispose) reads ch.rekeyMaterial, which we just
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
      if (rm === null || responderPub === undefined || responderPub.length !== X25519_PUBLIC_KEY_SIZE) {
        log.error('rekey ack missing local material or valid responder ephemeral; closing channel', {
          channel_id: ch.channelId,
          responder_pub_len: responderPub?.length ?? -1,
        })
        wipe()
        // Funnel teardown through rekeyAbort rather than hand-nulling rekeyWait:
        // rekeyAbort → clearRekeySlot clears the 10s timer + nulls
        // rekeyWait/rekeyClear/rekeyAbort/rekeyRequestId AND rejects the outcome
        // promise ensureRekeyed is awaiting. clearRekeySlot is idempotent, so the
        // abortRekey that endSession's dispose fires next is a no-op.
        ch.rekeyAbort?.()
        this.deps.endSession(ch, new ChannelError('transport', 'rekey ack carried no valid responder ephemeral key', { disconnected: false }))
        return
      }
      const { dhSecret, pqSecret } = deriveRekeySecrets(rm.ePriv, responderPub, rm.mlkemSS)
      const dhForRecv = dhSecret.slice()
      const pqForRecv = pqSecret !== null
        ? pqSecret.slice()
        : null
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

  /**
   * Encrypt `plaintext` and post its frames, all in this one synchronous loop, so
   * that the frames of one message stay contiguous and in nonce order: no other
   * frame, a RekeyRequest included, can fall between two of them. A do-while, so
   * that an empty message still sends its one frame. Throws on failure, after
   * the frames that it already posted.
   */
  private encryptAndSend(ch: SessionChannel, plaintext: Uint8Array, correlationId: number): void {
    let offset = 0
    do {
      const end = Math.min(offset + MAX_CHUNK_SIZE, plaintext.length)
      const chunk = plaintext.subarray(offset, end)
      offset = end
      const flags = offset < plaintext.length
        ? ChannelMessageFlags.MORE
        : ChannelMessageFlags.UNSPECIFIED
      const msg = create(ChannelMessageSchema, {
        channelId: ch.channelId,
        ciphertext: ch.session.send.encrypt(chunk),
        // uint64 on the wire; see the decode boundary in channelSessionInbound.ts
        // for why ids stay plain numbers on this side.
        correlationId: BigInt(correlationId),
        flags,
      })
      // Guarded like the receive-side sites: this runs for every outbound frame,
      // and Logger.debug evaluates its args (a fresh object literal) before
      // checking whether debug logging is on.
      if (log.isDebug())
        log.debug('sending channel message', { channel_id: ch.channelId, correlation_id: correlationId })
      this.deps.postFrame(ch.channelId, frameBytes(toBinary(ChannelMessageSchema, msg)))
    } while (offset < plaintext.length)
  }

  /**
   * Send the held lane, oldest first, and rekey first whenever a rekey is due.
   * One drain runs at a time; a send that arrives during it joins the lane.
   *
   * A held starting send whose id the main thread released (an abort, a timeout
   * or a cancel) never goes out, and neither does a later held send for that id:
   * the request it belongs to never reached the Worker.
   */
  private async drainHeld(ch: SessionChannel): Promise<void> {
    if (ch.draining)
      return
    ch.draining = true
    try {
      while (!ch.ended && ch.held.length > 0) {
        if (this.needsRekeyGate(ch)) {
          try {
            await this.ensureRekeyed(ch)
          }
          catch {
            // Every way that ensureRekeyed rejects has ended the session.
            return
          }
          if (ch.ended)
            return
        }
        const item = ch.held.shift()
        if (item === undefined)
          break
        if (item.startsRequest) {
          if (!ch.liveIds.has(item.correlationId)) {
            ch.abandonedIds.add(item.correlationId)
            continue
          }
        }
        else if (ch.abandonedIds.has(item.correlationId)) {
          continue
        }
        try {
          this.encryptAndSend(ch, item.plaintext, item.correlationId)
        }
        catch (err) {
          this.deps.endSession(ch, sendFailureError(err))
          return
        }
      }
    }
    finally {
      ch.draining = false
      if (ch.held.length === 0)
        ch.abandonedIds.clear()
    }
  }

  private async ensureRekeyedLocked(ch: SessionChannel): Promise<void> {
    if (ch.ended)
      return
    if (this.pastHardCeiling(ch)) {
      const err = new ChannelError('transport', 'session key past hard ceiling', { disconnected: false })
      this.deps.endSession(ch, err)
      throw err
    }
    if (!this.shouldInitiateRekey(ch))
      return

    const requestId = ch.nextRekeyId++
    const outcome = new Promise<{ accepted: boolean, retryAfterMs: number }>((resolve, reject) => {
      /* eslint-disable ts/no-use-before-define -- clearRekeySlot / timer mutually reference */
      const timer = setTimeout(() => {
        if (ch.rekeyWait) {
          // Clear the slot first, so that the dispose inside endSession finds
          // no rekeyAbort and does not reject `outcome` a second time.
          clearRekeySlot()
          const err = new ChannelError('transport', 'rekey timeout')
          this.deps.endSession(ch, err)
          reject(err)
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
        // dispose paths reach here instead, and must not leave the fresh
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
    // or an encrypt failure) tears down the waiter/timer just installed above and
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
      // Straight to the wire, past the held lane: the lane waits for this rekey.
      this.encryptAndSend(ch, toBinary(InnerMessageSchema, envelope), requestId)
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
      const failure = sendFailureError(err)
      this.deps.endSession(ch, failure)
      throw failure
    }

    const { accepted, retryAfterMs } = await outcome
    if (accepted) {
      ch.lastRekeyAt = monotonicNow()
      ch.rekeyNotBefore = 0
    }
    else {
      const backoff = retryAfterMs > 0
        ? retryAfterMs
        : DEFAULT_REJECT_BACKOFF_MS
      ch.rekeyNotBefore = monotonicNow() + backoff
      if (this.pastHardCeiling(ch)) {
        const err = new ChannelError('transport', 'session key past hard ceiling after rekey reject', { disconnected: false })
        this.deps.endSession(ch, err)
        throw err
      }
    }
  }
}
