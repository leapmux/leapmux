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

export class ChannelError extends Error {
  readonly source: ChannelErrorSource
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

  constructor(source: ChannelErrorSource, message: string, code = 0, fatal = false) {
    super(message)
    this.name = 'ChannelError'
    this.source = source
    this.code = code
    this.fatal = fatal
  }
}

/** Normalize an AbortSignal rejection into an Error for channel RPC callers. */
export function abortError(signal: AbortSignal, method: string): Error {
  return signal.reason instanceof Error
    ? signal.reason
    : new ChannelError('client', `RPC call '${method}' aborted`)
}
