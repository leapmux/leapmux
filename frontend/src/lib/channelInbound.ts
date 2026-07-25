/**
 * Inbound ChannelMessage dispatch: decrypt, correlation-id bounds, flags,
 * chunk reassembly, and InnerMessage routing (RPC / stream / rekey).
 *
 * Extracted from ChannelManager so open/verify coordination and message
 * handling can evolve independently. See https://github.com/leapmux/leapmux/issues/292.
 */
import type { ChannelRpcMux, RpcChannel } from './channelRpc'
import type { ChannelSession, SessionChannel } from './channelSession'
import type { ChannelMessage } from '~/generated/leapmux/v1/channel_pb'
import { fromBinary } from '@bufbuild/protobuf'
import {
  ChannelMessageFlags,
  InnerMessageSchema,
} from '~/generated/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { createLogger } from './logger'

const log = createLogger('channel')

/**
 * The largest wire correlation id this client will route. Ids are plain
 * numbers here, so anything past the exact-integer range is dropped rather
 * than rounded onto another request's handler.
 */
const MAX_SAFE_CORRELATION_ID = BigInt(Number.MAX_SAFE_INTEGER)

/** Channel shape inbound dispatch needs (RPC + session + lifecycle). */
export type InboundChannel = RpcChannel & SessionChannel & {
  state: 'opening' | 'verified' | 'closed'
}

export interface ChannelInboundDeps<T extends InboundChannel> {
  getChannel: (channelId: string) => T | undefined
  session: ChannelSession
  rpc: ChannelRpcMux
  closeChannel: (channelId: string) => void
  /**
   * Peer CLOSE flag: drain already ran; remove from the pool, stop idle timer
   * if empty, and notify state listeners.
   */
  forgetClosedChannel: (channelId: string) => void
}

export class ChannelInbound<T extends InboundChannel = InboundChannel> {
  constructor(private readonly deps: ChannelInboundDeps<T>) {}

  handleMessage(channelId: string, msg: ChannelMessage): void {
    const ch = this.deps.getChannel(channelId)
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
      this.deps.rpc.drainChannel(ch, new ChannelError('transport', 'channel closed by server'), 'error')
      ch.state = 'closed'
      this.deps.forgetClosedChannel(channelId)
      return
    }

    // Decrypt the ciphertext (session owns the receive cipher).
    let decrypted: Uint8Array
    try {
      decrypted = this.deps.session.decrypt(ch, msg.ciphertext)
    }
    catch (err) {
      log.error('Failed to decrypt channel message, closing channel', { channel_id: channelId, error: err })
      this.deps.closeChannel(channelId)
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
        this.deps.rpc.deliverResponse(ch, correlationId, envelope.kind.value)
        break
      case 'stream':
        this.deps.rpc.deliverStream(ch, correlationId, envelope.kind.value)
        break
      case 'rekeyAck':
        this.deps.session.handleRekeyOutcome(ch, true, 0, correlationId)
        break
      case 'rekeyReject': {
        const raw = envelope.kind.value.retryAfterMs
        const ms = typeof raw === 'bigint' ? Number(raw) : Number(raw ?? 0)
        this.deps.session.handleRekeyOutcome(ch, false, Number.isFinite(ms) && ms > 0 ? ms : 0, correlationId)
        break
      }
      case 'rekeyRequest':
        log.warn('ignored unexpected rekey request from peer', { channel_id: channelId })
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
    ch: T,
    correlationId: number,
    flags: ChannelMessageFlags,
    decrypted: Uint8Array,
  ): Uint8Array | null {
    const out = ch.reassembly.accept(
      correlationId,
      decrypted,
      flags === ChannelMessageFlags.MORE,
      id => this.deps.rpc.hasHandler(ch, id, ch.rekeyRequestId),
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
        log.error('Too many incomplete chunked messages', { channel_id: ch.channelId, correlation_id: correlationId })
        this.deps.rpc.failReassembly(ch, correlationId, 'too many incomplete chunked messages')
        return null
      case 'too-large':
        log.error('Chunked message exceeds max size', { channel_id: ch.channelId, correlation_id: correlationId, size: out.size })
        this.deps.rpc.failReassembly(ch, correlationId, `chunked message too large: ${out.size} bytes exceeds ${ch.maxReassembledMessageSize} byte limit`)
        return null
    }
  }
}
