import type { RpcChannel } from './channelRpc'
import type { InnerRpcResponse, InnerStreamMessage } from '~/generated/proto/leapmux/v1/channel_pb'
import { create } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { InnerRpcResponseSchema, InnerStreamMessageSchema } from '~/generated/proto/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { buildRequestPlaintext, buildStreamRequestPlaintext, ChannelRpcMux, FIRST_REQUEST_ID } from './channelRpc'
import { Reassembler } from './reassembler'

function safeCall(fn: () => void): void {
  try {
    fn()
  }
  catch {
    // ignore — mirrors production isolation
  }
}

function makeChannel(overrides?: Partial<RpcChannel>): RpcChannel {
  return {
    channelId: 'ch-1',
    workerId: 'w1',
    pendingRequests: new Map(),
    streamListeners: new Map(),
    reassembly: new Reassembler(1024),
    nextRequestId: 1,
    ...overrides,
  }
}

function makeMux(opts?: {
  send?: (ch: RpcChannel, plaintext: Uint8Array, requestId: number) => void
  onSendFailure?: (ch: RpcChannel, err: unknown) => void
  notifyError?: (workerId: string, error: ChannelError) => void
}) {
  const sent: { plaintext: Uint8Array, requestId: number }[] = []
  const mux = new ChannelRpcMux({
    send: opts?.send ?? ((_ch, plaintext, requestId) => {
      sent.push({ plaintext, requestId })
    }),
    onSendFailure: opts?.onSendFailure ?? (() => {}),
    rpcTimeoutFn: () => 50,
    notifyError: opts?.notifyError ?? (() => {}),
    safeCall: fn => safeCall(fn),
  })
  return { mux, sent }
}

describe('channelRpc', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('buildRequestPlaintext encodes method and payload', () => {
    const pt = buildRequestPlaintext('Ping', new Uint8Array([1, 2, 3]))
    expect(pt.length).toBeGreaterThan(0)
    // Same inputs must produce identical framing (call and stream share this).
    expect(buildRequestPlaintext('Ping', new Uint8Array([1, 2, 3]))).toEqual(pt)
  })

  it('callAfterRekey registers, sends, and resolves on deliverResponse', async () => {
    const ch = makeChannel()
    const { mux, sent } = makeMux()
    const p = mux.callAfterRekey(ch, 'ch-1', 'Echo', new Uint8Array([9]))
    expect(sent).toHaveLength(1)
    expect(sent[0]!.requestId).toBe(1)
    expect(ch.pendingRequests.has(1)).toBe(true)

    const resp = create(InnerRpcResponseSchema, {
      payload: new Uint8Array([42]),
    })
    mux.deliverResponse(ch, 1, resp)
    const resolved = await p
    expect(resolved.error, 'a response with no error resolves the call').toBeUndefined()
    expect(resolved.payload).toEqual(new Uint8Array([42]))
    expect(ch.pendingRequests.has(1)).toBe(false)
  })

  it('callAfterRekey rejects on timeout and unregisters', async () => {
    vi.useFakeTimers()
    const ch = makeChannel()
    const { mux } = makeMux()
    const p = mux.callAfterRekey(ch, 'ch-1', 'Slow', new Uint8Array())
    expect(ch.pendingRequests.has(1)).toBe(true)
    vi.advanceTimersByTime(50)
    await expect(p).rejects.toBeInstanceOf(ChannelError)
    expect(ch.pendingRequests.has(1)).toBe(false)
  })

  it('callAfterRekey abort drops the pending entry', async () => {
    const ch = makeChannel()
    const { mux } = makeMux()
    const ac = new AbortController()
    const p = mux.callAfterRekey(ch, 'ch-1', 'AbortMe', new Uint8Array(), undefined, ac.signal)
    expect(ch.pendingRequests.has(1)).toBe(true)
    ac.abort(new ChannelError('client', 'aborted by test'))
    await expect(p).rejects.toThrow('aborted by test')
    expect(ch.pendingRequests.has(1)).toBe(false)
  })

  it('callAfterRekey cleans up when send throws', async () => {
    const ch = makeChannel()
    const onSendFailure = vi.fn()
    const { mux } = makeMux({
      send: () => {
        throw new ChannelError('transport', 'ws dead')
      },
      onSendFailure,
    })
    await expect(mux.callAfterRekey(ch, 'ch-1', 'X', new Uint8Array())).rejects.toThrow('ws dead')
    expect(ch.pendingRequests.has(1)).toBe(false)
    expect(onSendFailure).toHaveBeenCalledOnce()
  })

  it('deliverResponse rejects rpc errors and notifies', async () => {
    const ch = makeChannel()
    const notifyError = vi.fn()
    const { mux } = makeMux({ notifyError })
    const p = mux.callAfterRekey(ch, 'ch-1', 'Fail', new Uint8Array())
    mux.deliverResponse(ch, 1, create(InnerRpcResponseSchema, {
      error: { code: 7, message: 'nope' },
    }))
    await expect(p).rejects.toMatchObject({ source: 'rpc', code: 7, message: 'nope' })
    expect(notifyError).toHaveBeenCalledOnce()
  })

  // The presence of the RpcError marks the failure, not a non-zero code: an
  // error with code 0 and no message still rejects, with a message that names
  // the code.
  it('deliverResponse rejects an rpc error that carries no message or code', async () => {
    const ch = makeChannel()
    const notifyError = vi.fn()
    const { mux } = makeMux({ notifyError })
    const p = mux.callAfterRekey(ch, 'ch-1', 'Fail', new Uint8Array())
    mux.deliverResponse(ch, 1, create(InnerRpcResponseSchema, {
      error: {},
      payload: new Uint8Array([1]),
    }))
    await expect(p).rejects.toMatchObject({ source: 'rpc', code: 0, message: 'RPC error code 0' })
    expect(notifyError).toHaveBeenCalledOnce()
    expect(ch.pendingRequests.has(1)).toBe(false)
  })

  it('deliverResponse names the code when an rpc error carries no message', async () => {
    const ch = makeChannel()
    const { mux } = makeMux()
    const p = mux.callAfterRekey(ch, 'ch-1', 'Fail', new Uint8Array())
    mux.deliverResponse(ch, 1, create(InnerRpcResponseSchema, {
      error: { code: 14 },
    }))
    await expect(p).rejects.toMatchObject({ source: 'rpc', code: 14, message: 'RPC error code 14' })
  })

  it('deliverResponse ignores unknown correlation ids', () => {
    const ch = makeChannel()
    const { mux } = makeMux()
    // Must not throw.
    mux.deliverResponse(ch, 99, create(InnerRpcResponseSchema, {
      payload: new Uint8Array(),
    }))
  })

  it('deliverResponse ignores a non-error unary reply on a stream id', () => {
    const ch = makeChannel()
    const onMessage = vi.fn()
    const onError = vi.fn()
    ch.streamListeners.set(5, {
      onMessage,
      onEnd: () => {},
      onError,
    })
    const { mux } = makeMux()
    mux.deliverResponse(ch, 5, create(InnerRpcResponseSchema, {
      payload: new Uint8Array([1]),
    }) as InnerRpcResponse)
    expect(onMessage).not.toHaveBeenCalled()
    expect(onError).not.toHaveBeenCalled()
    expect(ch.streamListeners.has(5)).toBe(true)
  })

  it('deliverResponse routes unary rpc error onto a stream listener', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    ch.streamListeners.set(5, {
      onMessage: () => {},
      onEnd: () => {},
      onError,
    })
    const { mux } = makeMux()
    mux.deliverResponse(ch, 5, create(InnerRpcResponseSchema, {
      error: { code: 12, message: 'unimplemented' },
    }) as InnerRpcResponse)
    expect(onError).toHaveBeenCalledOnce()
    expect(ch.streamListeners.has(5)).toBe(false)
  })

  it('beginStream attachAndSend registers and deliverStream ends', () => {
    const ch = makeChannel()
    const { mux, sent } = makeMux()
    const handle = mux.beginStream(ch, 'Subscribe', new Uint8Array([1]))
    expect(sent).toHaveLength(0)
    expect(handle.attachAndSend()).toBeNull()
    expect(sent).toHaveLength(1)
    expect(ch.streamListeners.has(handle.requestId)).toBe(true)

    const onMessage = vi.fn()
    const onEnd = vi.fn()
    handle.onMessage(onMessage)
    handle.onEnd(onEnd)
    mux.deliverStream(ch, handle.requestId, create(InnerStreamMessageSchema, {
      payload: new Uint8Array([7]),
    }) as InnerStreamMessage)
    expect(onMessage).toHaveBeenCalledOnce()
    expect(ch.streamListeners.has(handle.requestId)).toBe(true)

    mux.deliverStream(ch, handle.requestId, create(InnerStreamMessageSchema, {
      end: true,
    }) as InnerStreamMessage)
    expect(onEnd).toHaveBeenCalledOnce()
    expect(ch.streamListeners.has(handle.requestId)).toBe(false)
  })

  it('deliverStream errors and notifies', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    const notifyError = vi.fn()
    ch.streamListeners.set(3, {
      onMessage: () => {},
      onEnd: () => {},
      onError,
    })
    const { mux } = makeMux({ notifyError })
    mux.deliverStream(ch, 3, create(InnerStreamMessageSchema, {
      error: { code: 3, message: 'boom' },
    }) as InnerStreamMessage)
    expect(onError).toHaveBeenCalledOnce()
    expect(onError.mock.calls[0]?.[0]).toMatchObject({ source: 'stream', code: 3, message: 'boom' })
    expect(notifyError).toHaveBeenCalledOnce()
    expect(ch.streamListeners.has(3)).toBe(false)
  })

  it('deliverStream names the code when a stream error carries no message', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    const onEnd = vi.fn()
    ch.streamListeners.set(3, {
      onMessage: () => {},
      onEnd,
      onError,
    })
    const { mux } = makeMux()
    mux.deliverStream(ch, 3, create(InnerStreamMessageSchema, {
      error: {},
    }) as InnerStreamMessage)
    expect(onError).toHaveBeenCalledOnce()
    expect(onError.mock.calls[0]?.[0]).toMatchObject({ source: 'stream', code: 0, message: 'stream error code 0' })
    expect(onEnd).not.toHaveBeenCalled()
    expect(ch.streamListeners.has(3)).toBe(false)
  })

  // The unary error routed onto a stream listener keeps the rpc source.
  it('deliverResponse keeps the rpc source and code for an error on a stream id', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    ch.streamListeners.set(5, {
      onMessage: () => {},
      onEnd: () => {},
      onError,
    })
    const { mux } = makeMux()
    mux.deliverResponse(ch, 5, create(InnerRpcResponseSchema, {
      error: { code: 7 },
    }) as InnerRpcResponse)
    expect(onError.mock.calls[0]?.[0]).toMatchObject({ source: 'rpc', code: 7, message: 'RPC error code 7' })
  })

  it('drainChannel rejects pending and ends streams', () => {
    const ch = makeChannel()
    const reject = vi.fn()
    const onEnd = vi.fn()
    const onError = vi.fn()
    ch.pendingRequests.set(1, {
      resolve: () => {},
      reject,
    })
    ch.streamListeners.set(2, {
      onMessage: () => {},
      onEnd,
      onError,
    })
    ch.reassembly.start(1)
    const { mux } = makeMux()
    mux.drainChannel(ch, new ChannelError('client', 'channel closed'), 'end')
    expect(reject).toHaveBeenCalledOnce()
    expect(onEnd).toHaveBeenCalledOnce()
    expect(onError).not.toHaveBeenCalled()
    expect(ch.pendingRequests.size).toBe(0)
    expect(ch.streamListeners.size).toBe(0)
    expect(ch.reassembly.size()).toBe(0)
  })

  it('drainChannel with error termination calls onError', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    ch.streamListeners.set(1, {
      onMessage: () => {},
      onEnd: () => {},
      onError,
    })
    const { mux } = makeMux()
    mux.drainChannel(ch, new ChannelError('transport', 'disconnected'), 'error')
    expect(onError).toHaveBeenCalledOnce()
  })

  it('drainChannel keeps draining when a stream listener throws', () => {
    const ch = makeChannel()
    const reject = vi.fn()
    const onEndSecond = vi.fn()
    ch.pendingRequests.set(1, {
      resolve: () => {},
      reject,
    })
    ch.streamListeners.set(2, {
      onMessage: () => {},
      onEnd: () => {
        throw new Error('listener boom')
      },
      onError: () => {},
    })
    ch.streamListeners.set(3, {
      onMessage: () => {},
      onEnd: onEndSecond,
      onError: () => {},
    })
    const { mux } = makeMux()
    mux.drainChannel(ch, new ChannelError('client', 'channel closed'), 'end')
    expect(reject).toHaveBeenCalledOnce()
    expect(onEndSecond).toHaveBeenCalledOnce()
    expect(ch.pendingRequests.size).toBe(0)
    expect(ch.streamListeners.size).toBe(0)
  })

  it('unregisterRequest drops pending, stream, and reassembly together', () => {
    const ch = makeChannel()
    ch.pendingRequests.set(1, {
      resolve: () => {},
      reject: () => {},
    })
    ch.streamListeners.set(2, {
      onMessage: () => {},
      onEnd: () => {},
      onError: () => {},
    })
    ch.reassembly.start(1)
    ch.reassembly.start(2)
    const { mux } = makeMux()
    mux.unregisterRequest(ch, 1)
    mux.unregisterRequest(ch, 2)
    expect(ch.pendingRequests.has(1)).toBe(false)
    expect(ch.streamListeners.has(2)).toBe(false)
    expect(ch.reassembly.get(1)).toBeUndefined()
    expect(ch.reassembly.get(2)).toBeUndefined()
  })

  it('trySend wraps a non-ChannelError and unregisters', () => {
    const ch = makeChannel()
    ch.pendingRequests.set(1, {
      resolve: () => {},
      reject: () => {},
    })
    const onSendFailure = vi.fn()
    const { mux } = makeMux({
      send: () => {
        throw new Error('plain failure')
      },
      onSendFailure,
    })
    const err = mux.trySend(ch, new Uint8Array([1]), 1)
    expect(err).toBeInstanceOf(ChannelError)
    expect(err?.source).toBe('transport')
    expect(err?.message).toContain('plain failure')
    expect(ch.pendingRequests.has(1)).toBe(false)
    expect(onSendFailure).toHaveBeenCalledOnce()
  })

  it('failReassembly rejects and poisons the id', () => {
    const ch = makeChannel()
    const reject = vi.fn()
    ch.pendingRequests.set(1, {
      resolve: () => {},
      reject,
    })
    ch.reassembly.start(1)
    const { mux } = makeMux()
    mux.failReassembly(ch, 1, 'too large')
    expect(reject).toHaveBeenCalledOnce()
    expect(ch.reassembly.get(1)?.poisoned).toBe(true)
  })

  it('rejectPendingRequest errors a stream listener', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    ch.streamListeners.set(4, {
      onMessage: () => {},
      onEnd: () => {},
      onError,
    })
    const { mux } = makeMux()
    mux.rejectPendingRequest(ch, 4, 'client', 'cap')
    expect(onError).toHaveBeenCalledOnce()
    expect(ch.streamListeners.has(4)).toBe(false)
  })

  it('hasHandler covers pending, stream, and rekey ids', () => {
    const ch = makeChannel()
    ch.pendingRequests.set(1, {
      resolve: () => {},
      reject: () => {},
    })
    ch.streamListeners.set(2, {
      onMessage: () => {},
      onEnd: () => {},
      onError: () => {},
    })
    const { mux } = makeMux()
    expect(mux.hasHandler(ch, 1, null)).toBe(true)
    expect(mux.hasHandler(ch, 2, null)).toBe(true)
    expect(mux.hasHandler(ch, 9, 9)).toBe(true)
    expect(mux.hasHandler(ch, 3, 9)).toBe(false)
  })

  it('deliverDeferredError uses onError when set', () => {
    const ch = makeChannel()
    const { mux } = makeMux()
    const handle = mux.beginStream(ch, 'S', new Uint8Array())
    const onError = vi.fn()
    handle.onError(onError)
    handle.deliverDeferredError(new ChannelError('client', 'channel not open'), 'unused')
    expect(onError).toHaveBeenCalledOnce()
  })

  it('buildStreamRequestPlaintext round-trips cancel and payload', () => {
    const pt = buildStreamRequestPlaintext(new Uint8Array([1, 2]), true)
    expect(pt.length).toBeGreaterThan(0)
    const pt2 = buildStreamRequestPlaintext(new Uint8Array([1, 2]), true)
    expect(pt2).toEqual(pt)
  })

  it('sendOnStream reuses the stream correlation id and does not advance nextRequestId', () => {
    const ch = makeChannel({ nextRequestId: 5 })
    ch.streamListeners.set(3, {
      onMessage: () => {},
      onEnd: () => {},
      onError: () => {},
    })
    const { mux, sent } = makeMux()
    const err = mux.sendOnStream(ch, 3, new Uint8Array([7]), false)
    expect(err).toBeNull()
    expect(sent).toHaveLength(1)
    expect(sent[0]!.requestId).toBe(3)
    expect(ch.nextRequestId).toBe(5)
  })

  it('sendOnStream cancel unregisters locally', () => {
    const ch = makeChannel()
    ch.streamListeners.set(2, {
      onMessage: () => {},
      onEnd: () => {},
      onError: () => {},
    })
    const { mux } = makeMux()
    mux.sendOnStream(ch, 2, new Uint8Array(), true)
    expect(ch.streamListeners.has(2)).toBe(false)
  })

  it('sendOnStream on an unregistered id returns an error', () => {
    const ch = makeChannel()
    const { mux, sent } = makeMux()
    const err = mux.sendOnStream(ch, 99, new Uint8Array([1]), false)
    expect(err).not.toBeNull()
    expect(err?.message).toMatch(/not registered/)
    expect(sent).toHaveLength(0)
  })
})

describe('ChannelRpcMux wasAllocated', () => {
  it('reports nothing as allocated on a channel that has sent no request', () => {
    const ch = makeChannel({ nextRequestId: FIRST_REQUEST_ID })
    const { mux } = makeMux()
    expect(mux.wasAllocated(ch, 0)).toBe(false)
    expect(mux.wasAllocated(ch, FIRST_REQUEST_ID)).toBe(false)
  })

  it('reports every id from the first one up to the counter as allocated', () => {
    const ch = makeChannel({ nextRequestId: 5 })
    const { mux } = makeMux()
    expect([1, 2, 3, 4].map(id => mux.wasAllocated(ch, id))).toEqual([true, true, true, true])
  })

  it('reports the id that the counter hands out next as not allocated', () => {
    const ch = makeChannel({ nextRequestId: 5 })
    const { mux } = makeMux()
    expect(mux.wasAllocated(ch, 5)).toBe(false)
    expect(mux.wasAllocated(ch, 6)).toBe(false)
    expect(mux.wasAllocated(ch, Number.MAX_SAFE_INTEGER)).toBe(false)
  })

  it('reports ids below the first one as not allocated', () => {
    const ch = makeChannel({ nextRequestId: 5 })
    const { mux } = makeMux()
    expect(mux.wasAllocated(ch, 0)).toBe(false)
    expect(mux.wasAllocated(ch, -1)).toBe(false)
  })

  it('keeps reporting an id as allocated after its call retires', async () => {
    const ch = makeChannel()
    const { mux, sent } = makeMux()
    const controller = new AbortController()
    const call = mux.callAfterRekey(ch, 'ch-1', 'Ping', new Uint8Array(), undefined, controller.signal)
    const requestId = sent[0]!.requestId
    expect(mux.wasAllocated(ch, requestId)).toBe(true)

    controller.abort(new Error('caller left'))
    await expect(call).rejects.toThrow('caller left')

    expect(mux.hasHandler(ch, requestId, null)).toBe(false)
    expect(mux.wasAllocated(ch, requestId)).toBe(true)
  })

  it('counts a stream id as allocated once the stream begins', () => {
    const ch = makeChannel()
    const { mux } = makeMux()
    const handle = mux.beginStream(ch, 'WatchEvents', new Uint8Array())
    expect(handle.requestId).toBe(FIRST_REQUEST_ID)
    expect(mux.wasAllocated(ch, handle.requestId)).toBe(true)
  })
})
