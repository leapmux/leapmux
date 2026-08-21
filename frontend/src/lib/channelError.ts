/**
 * Structured error for encrypted-channel operations.
 *
 * Shared by ChannelManager and the extracted channel* seams so those modules
 * can construct/reject with ChannelError without importing the coordinator.
 */

/**
 * - `transport`: WebSocket disconnect/timeout, channel closed by server (connection-level)
 * - `stream`: Backend stream error (carries backend error code)
 * - `rpc`: Backend RPC error (carries backend error code)
 * - `client`: Client-side issues (channel not open, message too large)
 */
export type ChannelErrorSource = 'transport' | 'stream' | 'rpc' | 'client'

/**
 * The optional properties of a ChannelError.
 *
 * An options bag rather than trailing positional arguments: `fatal` and
 * `disconnected` are both booleans that mean opposite things, and the only site
 * that sets the second one would otherwise have to spell out defaults for the
 * two before it.
 */
export interface ChannelErrorOpts {
  /** See ChannelError.code. */
  code?: number
  /** See ChannelError.fatal. */
  fatal?: boolean
  /** See ChannelError.disconnected. */
  disconnected?: boolean
}

export class ChannelError extends Error {
  readonly source: ChannelErrorSource
  /** The peer's own error code, when the peer supplied one. Zero otherwise. */
  readonly code: number
  /**
   * The hub refused this connection outright and redialing cannot change that
   * (auth expiry/revocation, or the per-user connection cap).
   *
   * Callers distinguish this from an ordinary `transport` failure because the
   * two call for opposite responses: a normal disconnect means "retry, and tell
   * the user we are reconnecting", while this means "stop, and tell the user
   * what to do". Notably it is NOT evidence that the worker is unhealthy -- it
   * is a statement about our own connection allowance.
   */
  readonly fatal: boolean
  /**
   * There was no live link to carry the operation: the socket dropped, or the
   * channel it needed is gone.
   *
   * True for every `transport` failure, and for the one `client` failure that
   * reports connection state rather than a caller mistake -- see
   * `channelNotOpenError`. It stays false for the rest of `client`, where the
   * link is healthy and the call itself was refused (an over-size payload, an
   * aborted call, a per-RPC timeout).
   *
   * The flag exists so `isDisconnectError` can answer "did the link drop?" from
   * data instead of matching on the message text, which drifts the moment
   * anyone rewords a string.
   */
  readonly disconnected: boolean

  constructor(source: ChannelErrorSource, message: string, opts: ChannelErrorOpts = {}) {
    super(message)
    this.name = 'ChannelError'
    this.source = source
    this.code = opts.code ?? 0
    this.fatal = opts.fatal ?? false
    this.disconnected = opts.disconnected ?? source === 'transport'
  }
}

/**
 * The channel a caller asked for is not there: it was never opened, or a
 * teardown removed it while the caller waited.
 *
 * One factory, because the six sites that raise it must agree on both the copy
 * and the `disconnected` marker. The source stays `client` on purpose: the
 * socket may be perfectly healthy, and widening it to `transport` would feed
 * `isWorkerUnreachable`, which retires a tab on a positive offline reading.
 */
export function channelNotOpenError(): ChannelError {
  return new ChannelError('client', 'channel not open', { disconnected: true })
}

/** Normalize an AbortSignal rejection into an Error for channel RPC callers. */
export function abortError(signal: AbortSignal, method: string): Error {
  return signal.reason instanceof Error
    ? signal.reason
    : new ChannelError('client', `RPC call '${method}' aborted`)
}
