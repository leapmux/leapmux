/**
 * Inbound ChannelMessage handling in the channel session worker: decrypt,
 * correlation-id bounds, flags, chunk reassembly, and the rekey Ack/Reject
 * dispatch. A complete response or stream message goes to the main thread as a
 * `deliver` event, which routes it there (see channelInbound.ts).
 *
 * See https://github.com/leapmux/leapmux/issues/292.
 */
import type { ChannelSession, SessionChannel } from './channelSession'
import { fromBinary } from '@bufbuild/protobuf'
import { ChannelMessageFlags, InnerMessageSchema } from '~/generated/proto/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { FIRST_REKEY_CORRELATION_ID } from './channelSession'
import { FIRST_REQUEST_ID } from './channelSessionProtocol'
import { formatErrorMessage } from './errors'
import { createLogger } from './logger'

const log = createLogger('channel')

/**
 * The largest wire correlation id this client will route. Ids are plain
 * numbers here, so anything past the exact-integer range is dropped rather
 * than rounded onto another request's handler.
 */
export const MAX_SAFE_CORRELATION_ID = BigInt(Number.MAX_SAFE_INTEGER)

export interface ChannelSessionInboundDeps {
  session: ChannelSession
  /** Hand a complete response or stream message of `correlationId` to the main thread. */
  postDeliver: (ch: SessionChannel, correlationId: number, plaintext: Uint8Array) => void
  /** Tell the main thread that the inbound message of `correlationId` broke a reassembly limit. */
  postReassemblyFailed: (ch: SessionChannel, correlationId: number, message: string) => void
  /** End the session (see ChannelSessionDeps.endSession). */
  endSession: (ch: SessionChannel, error: ChannelError) => void
}

/**
 * Whether the channel ever allocated `correlationId`, whether its request is
 * still live or has retired.
 *
 * The main thread allocates request ids from FIRST_REQUEST_ID upward, and the
 * worker allocates RekeyRequest ids from FIRST_REKEY_CORRELATION_ID upward. An
 * inbound frame for an allocated id that has no consumer answers a request that
 * the main thread retired on purpose: an aborted or timed-out call, or a
 * cancelled stream whose worker still sends its end frame. A frame for any other
 * id is a protocol violation.
 */
function wasAllocated(ch: SessionChannel, correlationId: number): boolean {
  return (correlationId >= FIRST_REQUEST_ID && correlationId <= ch.highestStartedId)
    || (correlationId >= FIRST_REKEY_CORRELATION_ID && correlationId < ch.nextRekeyId)
}

export class ChannelSessionInbound {
  constructor(private readonly deps: ChannelSessionInboundDeps) {}

  /** Decrypt one inbound frame of `ch`, and act on the message that it completes. */
  handleInbound(ch: SessionChannel, correlationId: bigint, flags: ChannelMessageFlags, ciphertext: Uint8Array): void {
    if (ch.ended)
      return

    let decrypted: Uint8Array
    try {
      decrypted = this.deps.session.decrypt(ch, ciphertext)
    }
    catch (err) {
      log.error('Failed to decrypt channel message, closing channel', { channel_id: ch.channelId, error: err })
      this.deps.endSession(ch, new ChannelError('transport', `decrypt a channel message: ${formatErrorMessage(err)}`, { disconnected: false }))
      return
    }

    // correlation_id is uint64 on the wire so the id space cannot wrap (see
    // channel.proto), which protobuf-es surfaces as a bigint. Convert once here
    // rather than making every request registry, stream listener, and reassembly key
    // a bigint: request ids are allocated as a plain counter and never approach
    // 2^53, where a JS number stops being exact -- at the ~640 ids/sec a saturated
    // tunnel burns, reaching it takes ~450,000 years. RekeyRequest ids start at
    // FIRST_REKEY_CORRELATION_ID, 2^52, and a channel sends about one RekeyRequest
    // an hour, so they stay below 2^53 as well.
    //
    // A value past the safe range is therefore not one this client ever allocated, so
    // routing it would mean rounding it onto some OTHER request's handler. Drop the
    // message instead.
    //
    // This runs AFTER the decrypt, and must: Noise nonces are implicit and sequential,
    // so skipping a ciphertext leaves our receive nonce behind the peer's send nonce
    // and every subsequent message fails to decrypt. Dropping the plaintext costs one
    // message; dropping the ciphertext would cost the channel.
    if (correlationId > MAX_SAFE_CORRELATION_ID) {
      log.error('dropping channel message with an out-of-range correlation id', {
        channel_id: ch.channelId,
        correlation_id: String(correlationId),
      })
      return
    }
    const id = Number(correlationId)

    // An out-of-spec flags value (e.g. MORE|CLOSE combined, which no
    // conformant sender emits) is a protocol violation dropped here rather
    // than misread as "final chunk" -- which would hand a truncated assembly
    // to the InnerMessage decoder. Mirrors the Go receivers
    // (channelwire.ChunkContinuation); the main thread handles CLOSE and never
    // forwards it. Runs after the decrypt so the drop does not desync the
    // receive nonce.
    if (flags !== ChannelMessageFlags.UNSPECIFIED && flags !== ChannelMessageFlags.MORE) {
      log.warn('dropping channel message with out-of-spec flags', {
        channel_id: ch.channelId,
        correlation_id: id,
        flags,
      })
      return
    }

    // Feed the frame through chunk reassembly. A null result means it did not
    // complete a message (a buffered MORE chunk, or a dropped chunk for a
    // poisoned/unknown/over-cap id, or an oversize breach); a complete message
    // returns its full plaintext. Test with `=== null`, NOT falsiness: a zero-length
    // payload is a valid complete message that must still dispatch.
    const plaintext = this.reassemble(ch, id, flags, decrypted)
    if (plaintext === null)
      return
    this.dispatch(ch, id, plaintext)
  }

  /**
   * Act on one complete message: settle a rekey here, and hand a response or a
   * stream message to the main thread.
   */
  private dispatch(ch: SessionChannel, correlationId: number, plaintext: Uint8Array): void {
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
      case 'stream':
        this.deps.postDeliver(ch, correlationId, plaintext)
        break
      case 'rekeyAck':
        this.deps.session.handleRekeyOutcome(ch, true, 0, correlationId, envelope.kind.value.dhPub)
        break
      case 'rekeyReject': {
        const raw = envelope.kind.value.retryAfterMs
        const ms = typeof raw === 'bigint'
          ? Number(raw)
          : Number(raw ?? 0)
        this.deps.session.handleRekeyOutcome(ch, false, Number.isFinite(ms) && ms > 0
          ? ms
          : 0, correlationId)
        break
      }
      case 'rekeyRequest':
        log.warn('ignored unexpected rekey request from peer', { channel_id: ch.channelId })
        break
      default:
        log.warn('Unknown inner message type', envelope.kind.case)
    }
  }

  /**
   * Feed one decrypted frame into the correlation id's reassembly state and return
   * the complete message plaintext, or null when this frame did not complete one.
   * Callers MUST test the result with `=== null`, not falsiness.
   */
  private reassemble(
    ch: SessionChannel,
    correlationId: number,
    flags: ChannelMessageFlags,
    decrypted: Uint8Array,
  ): Uint8Array | null {
    const out = ch.reassembly.accept(
      correlationId,
      decrypted,
      flags === ChannelMessageFlags.MORE,
      id => ch.liveIds.has(id) || id === ch.rekeyRequestId,
    )
    switch (out.kind) {
      case 'deliver':
        return out.plaintext
      case 'buffered':
      case 'drop-poisoned':
        return null
      case 'drop-unknown':
        // The worker answers every request, so a request that this side stops
        // waiting for still gets its final frames: the response to an aborted or
        // timed-out call, or the end frame that answers a stream cancel. Those are
        // expected traffic, so they log at debug level. A duplicate reply to a
        // request that already completed lands here too, because nothing records
        // why an id retired. An id that this side never allocated is a protocol
        // violation and keeps the warning.
        if (wasAllocated(ch, correlationId))
          log.debug('dropped a late frame for a retired correlation id', { channel_id: ch.channelId, correlation_id: correlationId })
        else
          log.warn('dropped a frame for a correlation id that this client never allocated', { channel_id: ch.channelId, correlation_id: correlationId })
        return null
      case 'too-many':
        log.error('Too many incomplete chunked messages', { channel_id: ch.channelId, correlation_id: correlationId })
        this.failReassembly(ch, correlationId, 'too many incomplete chunked messages')
        return null
      case 'too-large':
        log.error('Chunked message exceeds max size', { channel_id: ch.channelId, correlation_id: correlationId, size: out.size })
        this.failReassembly(ch, correlationId, `chunked message too large: ${out.size} bytes exceeds ${ch.maxReassembledMessageSize} byte limit`)
        return null
    }
  }

  /**
   * Tombstone `correlationId`, so the rest of its chunks are dropped in silence,
   * then tell the main thread, which fails the request. The tombstone exists
   * before the main thread can release the id, and a release keeps it.
   */
  private failReassembly(ch: SessionChannel, correlationId: number, message: string): void {
    ch.reassembly.poison(correlationId)
    this.deps.postReassemblyFailed(ch, correlationId, message)
  }
}
