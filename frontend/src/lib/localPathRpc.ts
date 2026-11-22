import { Code } from '@connectrpc/connect'
import { ChannelError } from './channelError'

/**
 * True when a worker file RPC rejected with a path-level miss (`NotFound` or
 * `PermissionDenied`) that warrants trying the next resolved local path candidate.
 */
export function isLocalPathFallbackRpcError(error: unknown): boolean {
  return error instanceof ChannelError
    && error.source === 'rpc'
    && (error.code === Code.NotFound || error.code === Code.PermissionDenied)
}

/**
 * Extract a user-facing worker RPC error message, or `undefined` when the failure
 * came from transport/client layers or carried no message.
 */
export function localPathRpcErrorMessage(error: unknown): string | undefined {
  if (error instanceof ChannelError && error.source === 'rpc' && error.message)
    return error.message
  return undefined
}
