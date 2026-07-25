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

  constructor(source: ChannelErrorSource, message: string, code = 0) {
    super(message)
    this.name = 'ChannelError'
    this.source = source
    this.code = code
  }
}

/** Normalize an AbortSignal rejection into an Error for channel RPC callers. */
export function abortError(signal: AbortSignal, method: string): Error {
  return signal.reason instanceof Error
    ? signal.reason
    : new ChannelError('client', `RPC call '${method}' aborted`)
}
