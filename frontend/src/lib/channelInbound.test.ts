import type { InboundChannel } from './channelInbound'
import { create, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import {
  ChannelMessageFlags,
  ChannelMessageSchema,
  InnerMessageSchema,
  InnerRpcResponseSchema,
  InnerStreamMessageSchema,
  RekeyAckSchema,
} from '~/generated/proto/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { ChannelInbound } from './channelInbound'
import { ChannelRpcMux } from './channelRpc'

function makeChannel(overrides?: Partial<InboundChannel>): InboundChannel {
  return {
    channelId: 'ch-1',
    workerId: 'w1',
    pendingRequests: new Map(),
    streamListeners: new Map(),
    nextRequestId: 1,
    state: 'verified',
    ...overrides,
  }
}

function makeInbound(ch: InboundChannel) {
  const rpc = new ChannelRpcMux({
    send: () => {},
    release: () => {},
    onSendFailure: () => {},
    rpcTimeoutFn: () => 50,
    notifyError: () => {},
    safeCall: (fn) => {
      try {
        fn()
      }
      catch { /* isolate */ }
    },
  })
  const forwardInbound = vi.fn()
  const closedByPeer = vi.fn()
  const inbound = new ChannelInbound({
    getChannel: id => id === ch.channelId
      ? ch
      : undefined,
    rpc,
    session: { forwardInbound },
    closedByPeer,
  })
  return { inbound, forwardInbound, closedByPeer }
}

function frame(flags: ChannelMessageFlags, channelId = 'ch-1') {
  return create(ChannelMessageSchema, {
    channelId,
    ciphertext: new Uint8Array([1, 2, 3]),
    correlationId: 4n,
    flags,
  })
}

function responsePlaintext(): Uint8Array {
  return toBinary(InnerMessageSchema, create(InnerMessageSchema, {
    kind: {
      case: 'response',
      value: create(InnerRpcResponseSchema, {
        result: {
          case: 'payload',
          value: new Uint8Array([7]),
        },
      }),
    },
  }))
}

function streamPlaintext(): Uint8Array {
  return toBinary(InnerMessageSchema, create(InnerMessageSchema, {
    kind: {
      case: 'stream',
      value: create(InnerStreamMessageSchema, {
        payload: new Uint8Array([8]),
      }),
    },
  }))
}

describe('ChannelInbound handleMessage', () => {
  it('calls closedByPeer for a CLOSE frame, and forwards nothing', () => {
    const ch = makeChannel()
    const { inbound, forwardInbound, closedByPeer } = makeInbound(ch)

    inbound.handleMessage('ch-1', frame(ChannelMessageFlags.CLOSE))

    expect(closedByPeer).toHaveBeenCalledExactlyOnceWith('ch-1')
    expect(forwardInbound).not.toHaveBeenCalled()
  })

  it('ignores frames for unknown channel ids', () => {
    const ch = makeChannel()
    const { inbound, forwardInbound, closedByPeer } = makeInbound(ch)

    inbound.handleMessage('missing', frame(ChannelMessageFlags.UNSPECIFIED, 'missing'))
    inbound.handleMessage('missing', frame(ChannelMessageFlags.CLOSE, 'missing'))

    expect(forwardInbound).not.toHaveBeenCalled()
    expect(closedByPeer).not.toHaveBeenCalled()
  })

  it('forwards a data frame unchanged to the session worker', () => {
    const ch = makeChannel()
    const { inbound, forwardInbound } = makeInbound(ch)
    const more = frame(ChannelMessageFlags.MORE)
    const final = frame(ChannelMessageFlags.UNSPECIFIED)

    inbound.handleMessage('ch-1', more)
    inbound.handleMessage('ch-1', final)

    expect(forwardInbound.mock.calls).toEqual([
      ['ch-1', more],
      ['ch-1', final],
    ])
  })
})

describe('ChannelInbound deliver', () => {
  it('routes a response to its pending request', () => {
    const ch = makeChannel()
    const { inbound } = makeInbound(ch)
    const resolve = vi.fn()
    ch.pendingRequests.set(3, {
      resolve,
      reject: () => {},
    })

    inbound.deliver('ch-1', 3, responsePlaintext())

    expect(resolve).toHaveBeenCalledExactlyOnceWith(new Uint8Array([7]))
    expect(ch.pendingRequests.has(3)).toBe(false)
  })

  it('routes a stream message to its listener', () => {
    const ch = makeChannel()
    const { inbound } = makeInbound(ch)
    const onMessage = vi.fn()
    ch.streamListeners.set(5, {
      onMessage,
      onEnd: () => {},
      onError: () => {},
    })

    inbound.deliver('ch-1', 5, streamPlaintext())

    expect(onMessage).toHaveBeenCalledOnce()
    expect(onMessage.mock.calls[0]![0].payload).toEqual(new Uint8Array([8]))
  })

  it('drops a message for a closed or a missing channel', () => {
    const ch = makeChannel({ state: 'closed' })
    const { inbound } = makeInbound(ch)
    const resolve = vi.fn()
    ch.pendingRequests.set(3, {
      resolve,
      reject: () => {},
    })

    inbound.deliver('ch-1', 3, responsePlaintext())
    inbound.deliver('missing', 3, responsePlaintext())

    expect(resolve).not.toHaveBeenCalled()
    expect(ch.pendingRequests.has(3)).toBe(true)
  })

  it('logs and drops an envelope that does not decode', () => {
    const ch = makeChannel()
    const { inbound } = makeInbound(ch)
    const resolve = vi.fn()
    ch.pendingRequests.set(3, {
      resolve,
      reject: () => {},
    })
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    // A truncated varint, which no envelope decoder accepts.
    inbound.deliver('ch-1', 3, Uint8Array.of(0x0A, 0xFF))

    expect(errorSpy).toHaveBeenCalledWith('[channel]', 'Failed to deserialize InnerMessage', expect.anything())
    expect(resolve).not.toHaveBeenCalled()
    errorSpy.mockRestore()
  })

  it('warns about an envelope kind that the main thread does not route', () => {
    const ch = makeChannel()
    const { inbound } = makeInbound(ch)
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})

    inbound.deliver('ch-1', 3, toBinary(InnerMessageSchema, create(InnerMessageSchema, {
      kind: {
        case: 'rekeyAck',
        value: create(RekeyAckSchema, {}),
      },
    })))

    expect(warnSpy).toHaveBeenCalledWith('[channel]', 'Unknown inner message type', 'rekeyAck')
    warnSpy.mockRestore()
  })
})

describe('ChannelInbound reassemblyFailed', () => {
  it('rejects the pending request with a client error that carries the message', async () => {
    const ch = makeChannel()
    const { inbound } = makeInbound(ch)
    const reject = vi.fn()
    ch.pendingRequests.set(3, {
      resolve: () => {},
      reject,
    })

    inbound.reassemblyFailed('ch-1', 3, 'too many incomplete chunked messages')

    expect(reject).toHaveBeenCalledOnce()
    const err = reject.mock.calls[0]![0] as ChannelError
    expect(err).toBeInstanceOf(ChannelError)
    expect(err.source).toBe('client')
    expect(err.message).toBe('too many incomplete chunked messages')
    expect(ch.pendingRequests.has(3)).toBe(false)
  })

  it('errors a stream listener that waits on the id', () => {
    const ch = makeChannel()
    const { inbound } = makeInbound(ch)
    const onError = vi.fn()
    ch.streamListeners.set(4, {
      onMessage: () => {},
      onEnd: () => {},
      onError,
    })

    inbound.reassemblyFailed('ch-1', 4, 'chunked message too large: 60 bytes exceeds 50 byte limit')

    expect(onError).toHaveBeenCalledOnce()
    expect(onError.mock.calls[0]![0]).toMatchObject({ source: 'client', message: 'chunked message too large: 60 bytes exceeds 50 byte limit' })
  })

  it('does nothing for a missing channel', () => {
    const ch = makeChannel()
    const { inbound } = makeInbound(ch)
    expect(() => inbound.reassemblyFailed('missing', 3, 'too many incomplete chunked messages')).not.toThrow()
  })
})
