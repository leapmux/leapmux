import type { RpcChannel } from './channelRpc'
import type { InnerRpcResponse, InnerStreamMessage } from '~/generated/leapmux/v1/channel_pb'
import { create } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { InnerRpcResponseSchema, InnerStreamMessageSchema } from '~/generated/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { buildRequestPlaintext, ChannelRpcMux } from './channelRpc'
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
      isError: false,
      payload: new Uint8Array([42]),
    })
    mux.deliverResponse(ch, 1, resp)
    await expect(p).resolves.toMatchObject({ isError: false, payload: new Uint8Array([42]) })
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
      isError: true,
      errorCode: 7,
      errorMessage: 'nope',
    }))
    await expect(p).rejects.toMatchObject({ source: 'rpc', code: 7, message: 'nope' })
    expect(notifyError).toHaveBeenCalledOnce()
  })

  it('deliverResponse ignores unknown correlation ids', () => {
    const ch = makeChannel()
    const { mux } = makeMux()
    // Must not throw.
    mux.deliverResponse(ch, 99, create(InnerRpcResponseSchema, { isError: false, payload: new Uint8Array() }))
  })

  it('deliverResponse ignores a non-error unary reply on a stream id', () => {
    const ch = makeChannel()
    const onMessage = vi.fn()
    const onError = vi.fn()
    ch.streamListeners.set(5, { onMessage, onEnd: () => {}, onError })
    const { mux } = makeMux()
    mux.deliverResponse(ch, 5, create(InnerRpcResponseSchema, {
      isError: false,
      payload: new Uint8Array([1]),
    }) as InnerRpcResponse)
    expect(onMessage).not.toHaveBeenCalled()
    expect(onError).not.toHaveBeenCalled()
    expect(ch.streamListeners.has(5)).toBe(true)
  })

  it('deliverResponse routes unary rpc error onto a stream listener', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    ch.streamListeners.set(5, { onMessage: () => {}, onEnd: () => {}, onError })
    const { mux } = makeMux()
    mux.deliverResponse(ch, 5, create(InnerRpcResponseSchema, {
      isError: true,
      errorCode: 12,
      errorMessage: 'unimplemented',
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
    ch.streamListeners.set(3, { onMessage: () => {}, onEnd: () => {}, onError })
    const { mux } = makeMux({ notifyError })
    mux.deliverStream(ch, 3, create(InnerStreamMessageSchema, {
      isError: true,
      errorCode: 3,
      errorMessage: 'boom',
    }) as InnerStreamMessage)
    expect(onError).toHaveBeenCalledOnce()
    expect(notifyError).toHaveBeenCalledOnce()
    expect(ch.streamListeners.has(3)).toBe(false)
  })

  it('drainChannel rejects pending and ends streams', () => {
    const ch = makeChannel()
    const reject = vi.fn()
    const onEnd = vi.fn()
    const onError = vi.fn()
    ch.pendingRequests.set(1, { resolve: () => {}, reject })
    ch.streamListeners.set(2, { onMessage: () => {}, onEnd, onError })
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
    ch.streamListeners.set(1, { onMessage: () => {}, onEnd: () => {}, onError })
    const { mux } = makeMux()
    mux.drainChannel(ch, new ChannelError('transport', 'disconnected'), 'error')
    expect(onError).toHaveBeenCalledOnce()
  })

  it('drainChannel keeps draining when a stream listener throws', () => {
    const ch = makeChannel()
    const reject = vi.fn()
    const onEndSecond = vi.fn()
    ch.pendingRequests.set(1, { resolve: () => {}, reject })
    ch.streamListeners.set(2, {
      onMessage: () => {},
      onEnd: () => {
        throw new Error('listener boom')
      },
      onError: () => {},
    })
    ch.streamListeners.set(3, { onMessage: () => {}, onEnd: onEndSecond, onError: () => {} })
    const { mux } = makeMux()
    mux.drainChannel(ch, new ChannelError('client', 'channel closed'), 'end')
    expect(reject).toHaveBeenCalledOnce()
    expect(onEndSecond).toHaveBeenCalledOnce()
    expect(ch.pendingRequests.size).toBe(0)
    expect(ch.streamListeners.size).toBe(0)
  })

  it('unregisterRequest drops pending, stream, and reassembly together', () => {
    const ch = makeChannel()
    ch.pendingRequests.set(1, { resolve: () => {}, reject: () => {} })
    ch.streamListeners.set(2, { onMessage: () => {}, onEnd: () => {}, onError: () => {} })
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
    ch.pendingRequests.set(1, { resolve: () => {}, reject: () => {} })
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
    ch.pendingRequests.set(1, { resolve: () => {}, reject })
    ch.reassembly.start(1)
    const { mux } = makeMux()
    mux.failReassembly(ch, 1, 'too large')
    expect(reject).toHaveBeenCalledOnce()
    expect(ch.reassembly.get(1)?.poisoned).toBe(true)
  })

  it('rejectPendingRequest errors a stream listener', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    ch.streamListeners.set(4, { onMessage: () => {}, onEnd: () => {}, onError })
    const { mux } = makeMux()
    mux.rejectPendingRequest(ch, 4, 'client', 'cap')
    expect(onError).toHaveBeenCalledOnce()
    expect(ch.streamListeners.has(4)).toBe(false)
  })

  it('hasHandler covers pending, stream, and rekey ids', () => {
    const ch = makeChannel()
    ch.pendingRequests.set(1, { resolve: () => {}, reject: () => {} })
    ch.streamListeners.set(2, { onMessage: () => {}, onEnd: () => {}, onError: () => {} })
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
})
