/**
 * Inbound ChannelMessage dispatch on the main thread: the peer's CLOSE flag,
 * forwarding each ciphertext to the channel session worker, and routing what
 * the worker decrypts (RPC / stream) to the RPC mux. The decrypt, the
 * correlation-id bounds, the flags check and the chunk reassembly run in the
 * worker (see channelSessionInbound.ts).
 *
 * Extracted from ChannelManager so open/verify coordination and message
 * handling can evolve independently. See https://github.com/leapmux/leapmux/issues/292.
 */
import type { ChannelRpcMux, RpcChannel } from './channelRpc'
import type { ChannelSessionClient } from './channelSessionClient'
import type { ChannelMessage } from '~/generated/proto/leapmux/v1/channel_pb'
import { fromBinary } from '@bufbuild/protobuf'
import {
  ChannelMessageFlags,
  InnerMessageSchema,
} from '~/generated/proto/leapmux/v1/channel_pb'
import { createLogger } from './logger'

const log = createLogger('channel')

/** Channel shape inbound dispatch needs (RPC + lifecycle). */
export type InboundChannel = RpcChannel & {
  state: 'opening' | 'verified' | 'closed'
}

export interface ChannelInboundDeps<T extends InboundChannel> {
  getChannel: (channelId: string) => T | undefined
  rpc: ChannelRpcMux
  session: Pick<ChannelSessionClient, 'forwardInbound'>
  /** The peer sent the CLOSE flag: retire the channel, and notify state listeners. */
  closedByPeer: (channelId: string) => void
}

export class ChannelInbound<T extends InboundChannel = InboundChannel> {
  constructor(private readonly deps: ChannelInboundDeps<T>) {}

  /** Act on one frame from the relay. */
  handleMessage(channelId: string, msg: ChannelMessage): void {
    const ch = this.deps.getChannel(channelId)
    if (!ch)
      return

    // Guard the payload build behind isDebug: handleMessage runs for every inbound
    // frame (RPC response, stream chunk, tunnel data/credit), and the debug args --
    // a fresh object literal plus a bigint->string conversion -- are evaluated at the
    // call site regardless of whether debug logging is on (see Logger.debug).
    if (log.isDebug()) {
      log.debug('received channel message', {
        channel_id: channelId,
        correlation_id: String(msg.correlationId),
      })
    }

    // Close sentinel: CLOSE flag. It carries no ciphertext, so it never goes to
    // the session worker.
    if (msg.flags === ChannelMessageFlags.CLOSE) {
      this.deps.closedByPeer(channelId)
      return
    }

    this.deps.session.forwardInbound(channelId, msg)
  }

  /** Route one message that the session worker decrypted and reassembled. */
  deliver(channelId: string, correlationId: number, plaintext: Uint8Array): void {
    const ch = this.deps.getChannel(channelId)
    if (!ch || ch.state === 'closed')
      return

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
      default:
        log.warn('Unknown inner message type', envelope.kind.case)
    }
  }

  /**
   * An inbound chunked message broke a reassembly limit in the session worker,
   * which already tombstoned its id: fail the request that waits on it.
   */
  reassemblyFailed(channelId: string, correlationId: number, message: string): void {
    const ch = this.deps.getChannel(channelId)
    if (!ch)
      return
    this.deps.rpc.rejectPendingRequest(ch, correlationId, 'client', message)
  }
}
