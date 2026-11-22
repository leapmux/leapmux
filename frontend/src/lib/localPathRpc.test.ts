import { Code } from '@connectrpc/connect'
import { describe, expect, it } from 'vitest'
import { ChannelError } from './channelError'
import { isLocalPathFallbackRpcError, localPathRpcErrorMessage } from './localPathRpc'

describe('isLocalPathFallbackRpcError', () => {
  it('returns true for rpc NotFound and PermissionDenied ChannelErrors', () => {
    expect(isLocalPathFallbackRpcError(
      new ChannelError('rpc', 'not found', { code: Code.NotFound }),
    )).toBe(true)
    expect(isLocalPathFallbackRpcError(
      new ChannelError('rpc', 'denied', { code: Code.PermissionDenied }),
    )).toBe(true)
  })

  it('returns false for other rpc codes, non-rpc ChannelErrors, and plain errors', () => {
    expect(isLocalPathFallbackRpcError(
      new ChannelError('rpc', 'internal', { code: Code.Internal }),
    )).toBe(false)
    expect(isLocalPathFallbackRpcError(
      new ChannelError('rpc', 'unavailable', { code: Code.Unavailable }),
    )).toBe(false)
    expect(isLocalPathFallbackRpcError(
      new ChannelError('transport', 'disconnected', { code: Code.NotFound }),
    )).toBe(false)
    expect(isLocalPathFallbackRpcError(
      new ChannelError('client', 'channel not open', { code: Code.NotFound }),
    )).toBe(false)
    expect(isLocalPathFallbackRpcError(new Error('not found'))).toBe(false)
    expect(isLocalPathFallbackRpcError(null)).toBe(false)
    expect(isLocalPathFallbackRpcError('not found')).toBe(false)
  })
})

describe('localPathRpcErrorMessage', () => {
  it('returns the message for rpc ChannelErrors with a non-empty message', () => {
    expect(localPathRpcErrorMessage(
      new ChannelError('rpc', 'permission denied', { code: Code.PermissionDenied }),
    )).toBe('permission denied')
  })

  it('returns undefined for empty rpc messages, non-rpc ChannelErrors, and unknown values', () => {
    expect(localPathRpcErrorMessage(
      new ChannelError('rpc', '', { code: Code.Internal }),
    )).toBeUndefined()
    expect(localPathRpcErrorMessage(
      new ChannelError('transport', 'socket closed'),
    )).toBeUndefined()
    expect(localPathRpcErrorMessage(
      new ChannelError('client', 'channel not open'),
    )).toBeUndefined()
    expect(localPathRpcErrorMessage(new Error('plain error'))).toBeUndefined()
    expect(localPathRpcErrorMessage(undefined)).toBeUndefined()
  })
})
