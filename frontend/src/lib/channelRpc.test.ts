import type { RpcChannel } from './channelRpc'
import type { InnerRpcResponse, InnerStreamMessage } from '~/generated/proto/leapmux/v1/channel_pb'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { InnerMessageSchema, InnerRpcResponseSchema, InnerStreamMessageSchema } from '~/generated/proto/leapmux/v1/channel_pb'
import { ChannelError } from './channelError'
import { buildRequestPlaintext, buildStreamRequestPlaintext, ChannelRpcMux } from './channelRpc'

/** Decode the action of a stream frame from its InnerMessage plaintext. */
function decodeStreamAction(plaintext: Uint8Array) {
  const { kind } = fromBinary(InnerMessageSchema, plaintext)
  if (kind.case !== 'streamRequest')
    throw new Error(`expected a stream request, got ${kind.case}`)
  return kind.value.action
}

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
    nextRequestId: 1,
    ...overrides,
  }
}

/** One call of the mux into the session, in call order. */
type SessionCall
  = | { op: 'send', plaintext: Uint8Array, requestId: number, startsRequest: boolean }
    | { op: 'release', correlationId: number }

function makeMux(opts?: {
  send?: (ch: RpcChannel, plaintext: Uint8Array, requestId: number, startsRequest: boolean) => void
  onSendFailure?: (ch: RpcChannel, err: unknown) => void
  notifyError?: (workerId: string, error: ChannelError) => void
}) {
  const calls: SessionCall[] = []
  const mux = new ChannelRpcMux({
    send: opts?.send ?? ((_ch, plaintext, requestId, startsRequest) => {
      calls.push({
        op: 'send',
        plaintext,
        requestId,
        startsRequest,
      })
    }),
    release: (_ch, correlationId) => {
      calls.push({ op: 'release', correlationId })
    },
    onSendFailure: opts?.onSendFailure ?? (() => {}),
    rpcTimeoutFn: () => 50,
    notifyError: opts?.notifyError ?? (() => {}),
    safeCall: fn => safeCall(fn),
  })
  /** The sends, in call order. */
  const sends = () => calls.filter((call): call is Extract<SessionCall, { op: 'send' }> => call.op === 'send')
  /** The released ids, in call order. */
  const released = () => calls.flatMap(call => call.op === 'release'
    ? [call.correlationId]
    : [])
  return { mux, sends, released, calls }
}

describe('channelRpc', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('encodes method and payload in buildRequestPlaintext', () => {
    const pt = buildRequestPlaintext('Ping', new Uint8Array([1, 2, 3]))
    expect(pt.length).toBeGreaterThan(0)
    // Same inputs must produce identical framing (call and stream share this).
    expect(buildRequestPlaintext('Ping', new Uint8Array([1, 2, 3]))).toEqual(pt)
  })

  it('registers, sends as a starting send, and resolves a call when deliverResponse delivers the response', async () => {
    const ch = makeChannel()
    const { mux, sends, released } = makeMux()
    const p = mux.call(ch, 'Echo', new Uint8Array([9]))
    expect(sends()).toHaveLength(1)
    expect(sends()[0]!.requestId).toBe(1)
    expect(sends()[0]!.startsRequest).toBe(true)
    expect(ch.pendingRequests.has(1)).toBe(true)

    const resp = create(InnerRpcResponseSchema, {
      result: {
        case: 'payload',
        value: new Uint8Array([42]),
      },
    })
    expect(released()).toEqual([])
    mux.deliverResponse(ch, 1, resp)
    await expect(p).resolves.toEqual(new Uint8Array([42]))
    expect(ch.pendingRequests.has(1)).toBe(false)
    // The session drops what it kept for the id, such as a reassembly buffer.
    expect(released()).toEqual([1])
  })

  // A response that selects no result is the empty success, as a worker's Ping
  // answer is, and an empty payload is a success too.
  it('resolves a call with empty bytes for a response with no result or an empty payload', async () => {
    const ch = makeChannel()
    const { mux } = makeMux()

    const noResult = mux.call(ch, 'Ping', new Uint8Array())
    mux.deliverResponse(ch, 1, create(InnerRpcResponseSchema, {}))
    await expect(noResult).resolves.toEqual(new Uint8Array())

    const emptyPayload = mux.call(ch, 'Ping', new Uint8Array())
    mux.deliverResponse(ch, 2, create(InnerRpcResponseSchema, {
      result: {
        case: 'payload',
        value: new Uint8Array(),
      },
    }))
    await expect(emptyPayload).resolves.toEqual(new Uint8Array())
  })

  it('rejects on timeout and unregisters in call', async () => {
    vi.useFakeTimers()
    const ch = makeChannel()
    const { mux, released } = makeMux()
    const p = mux.call(ch, 'Slow', new Uint8Array())
    expect(ch.pendingRequests.has(1)).toBe(true)
    vi.advanceTimersByTime(50)
    await expect(p).rejects.toBeInstanceOf(ChannelError)
    await expect(p).rejects.toThrow(`RPC call 'Slow' timed out after 0s (channel=ch-1)`)
    expect(ch.pendingRequests.has(1)).toBe(false)
    expect(released()).toEqual([1])
  })

  it('drops the pending entry and releases the id when call is aborted', async () => {
    const ch = makeChannel()
    const { mux, released } = makeMux()
    const ac = new AbortController()
    const p = mux.call(ch, 'AbortMe', new Uint8Array(), undefined, ac.signal)
    expect(ch.pendingRequests.has(1)).toBe(true)
    ac.abort(new ChannelError('client', 'aborted by test'))
    await expect(p).rejects.toThrow('aborted by test')
    expect(ch.pendingRequests.has(1)).toBe(false)
    expect(released()).toEqual([1])
  })

  it('cleans up in call when send throws', async () => {
    const ch = makeChannel()
    const onSendFailure = vi.fn()
    const { mux, released } = makeMux({
      send: () => {
        throw new ChannelError('transport', 'ws dead')
      },
      onSendFailure,
    })
    await expect(mux.call(ch, 'X', new Uint8Array())).rejects.toThrow('ws dead')
    expect(ch.pendingRequests.has(1)).toBe(false)
    expect(released()).toEqual([1])
    expect(onSendFailure).toHaveBeenCalledOnce()
  })

  it('rejects rpc errors and notifies in deliverResponse', async () => {
    const ch = makeChannel()
    const notifyError = vi.fn()
    const { mux, released } = makeMux({ notifyError })
    const p = mux.call(ch, 'Fail', new Uint8Array())
    mux.deliverResponse(ch, 1, create(InnerRpcResponseSchema, {
      result: {
        case: 'error',
        value: { code: 7, message: 'nope' },
      },
    }))
    await expect(p).rejects.toMatchObject({ source: 'rpc', code: 7, message: 'nope' })
    expect(notifyError).toHaveBeenCalledOnce()
    expect(released()).toEqual([1])
  })

  // The error case marks the failure, not a non-zero code: an error with code 0
  // and no message still rejects, with a message that states the code. The case
  // survives the wire, where every field of that error has its default value.
  it('rejects an rpc error that carries no message or code in deliverResponse', async () => {
    const ch = makeChannel()
    const notifyError = vi.fn()
    const { mux } = makeMux({ notifyError })
    const p = mux.call(ch, 'Fail', new Uint8Array())
    const encoded = toBinary(InnerRpcResponseSchema, create(InnerRpcResponseSchema, {
      result: { case: 'error', value: {} },
    }))
    mux.deliverResponse(ch, 1, fromBinary(InnerRpcResponseSchema, encoded))
    await expect(p).rejects.toMatchObject({ source: 'rpc', code: 0, message: 'RPC error code 0' })
    expect(notifyError).toHaveBeenCalledOnce()
    expect(ch.pendingRequests.has(1)).toBe(false)
  })

  it('states the code in deliverResponse when an rpc error carries no message', async () => {
    const ch = makeChannel()
    const { mux } = makeMux()
    const p = mux.call(ch, 'Fail', new Uint8Array())
    mux.deliverResponse(ch, 1, create(InnerRpcResponseSchema, {
      result: {
        case: 'error',
        value: { code: 14 },
      },
    }))
    await expect(p).rejects.toMatchObject({ source: 'rpc', code: 14, message: 'RPC error code 14' })
  })

  it('ignores unknown correlation ids in deliverResponse', () => {
    const ch = makeChannel()
    const { mux } = makeMux()
    // Must not throw.
    mux.deliverResponse(ch, 99, create(InnerRpcResponseSchema, {
      result: {
        case: 'payload',
        value: new Uint8Array(),
      },
    }))
  })

  it('ignores a non-error unary reply on a stream id in deliverResponse', () => {
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
      result: {
        case: 'payload',
        value: new Uint8Array([1]),
      },
    }) as InnerRpcResponse)
    expect(onMessage).not.toHaveBeenCalled()
    expect(onError).not.toHaveBeenCalled()
    expect(ch.streamListeners.has(5)).toBe(true)
  })

  it('routes a unary rpc error onto a stream listener in deliverResponse', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    ch.streamListeners.set(5, {
      onMessage: () => {},
      onEnd: () => {},
      onError,
    })
    const { mux } = makeMux()
    mux.deliverResponse(ch, 5, create(InnerRpcResponseSchema, {
      result: {
        case: 'error',
        value: { code: 12, message: 'unimplemented' },
      },
    }) as InnerRpcResponse)
    expect(onError).toHaveBeenCalledOnce()
    expect(ch.streamListeners.has(5)).toBe(false)
  })

  it('registers and sends a stream in beginStream, and deliverStream ends it', () => {
    const ch = makeChannel()
    const { mux, sends, released } = makeMux()
    const handle = mux.beginStream(ch, 'Subscribe', new Uint8Array([1]))
    expect(sends()).toHaveLength(1)
    expect(sends()[0]).toMatchObject({ requestId: handle.requestId, startsRequest: true })
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
    expect(released()).toEqual([])

    mux.deliverStream(ch, handle.requestId, create(InnerStreamMessageSchema, {
      end: true,
    }) as InnerStreamMessage)
    expect(onEnd).toHaveBeenCalledOnce()
    expect(ch.streamListeners.has(handle.requestId)).toBe(false)
    expect(released()).toEqual([handle.requestId])
  })

  it('throws a send error from beginStream, and leaves no listener', () => {
    const ch = makeChannel()
    const onSendFailure = vi.fn()
    const { mux, released } = makeMux({
      send: () => {
        throw new ChannelError('transport', 'channel session worker is not running')
      },
      onSendFailure,
    })

    expect(() => mux.beginStream(ch, 'Subscribe', new Uint8Array([1]))).toThrow('channel session worker is not running')

    expect(ch.streamListeners.size).toBe(0)
    expect(released()).toEqual([1])
    expect(onSendFailure).toHaveBeenCalledOnce()
    // The id is spent, so the next request takes a fresh one.
    expect(ch.nextRequestId).toBe(2)
  })

  it('reports the stream error to the listener and notifies in deliverStream', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    const notifyError = vi.fn()
    ch.streamListeners.set(3, {
      onMessage: () => {},
      onEnd: () => {},
      onError,
    })
    const { mux, released } = makeMux({ notifyError })
    mux.deliverStream(ch, 3, create(InnerStreamMessageSchema, {
      error: { code: 3, message: 'boom' },
    }) as InnerStreamMessage)
    expect(onError).toHaveBeenCalledOnce()
    expect(onError.mock.calls[0]?.[0]).toMatchObject({ source: 'stream', code: 3, message: 'boom' })
    expect(notifyError).toHaveBeenCalledOnce()
    expect(ch.streamListeners.has(3)).toBe(false)
    expect(released()).toEqual([3])
  })

  it('states the code in deliverStream when a stream error carries no message', () => {
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
  it('keeps the rpc source and code for an error on a stream id in deliverResponse', () => {
    const ch = makeChannel()
    const onError = vi.fn()
    ch.streamListeners.set(5, {
      onMessage: () => {},
      onEnd: () => {},
      onError,
    })
    const { mux } = makeMux()
    mux.deliverResponse(ch, 5, create(InnerRpcResponseSchema, {
      result: {
        case: 'error',
        value: { code: 7 },
      },
    }) as InnerRpcResponse)
    expect(onError.mock.calls[0]?.[0]).toMatchObject({ source: 'rpc', code: 7, message: 'RPC error code 7' })
  })

  it('rejects pending and ends streams in drainChannel', () => {
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
    const { mux, released } = makeMux()
    mux.drainChannel(ch, new ChannelError('client', 'channel closed'), 'end')
    expect(reject).toHaveBeenCalledOnce()
    expect(onEnd).toHaveBeenCalledOnce()
    expect(onError).not.toHaveBeenCalled()
    expect(ch.pendingRequests.size).toBe(0)
    expect(ch.streamListeners.size).toBe(0)
    // The session drops the channel's buffers on its own close; a drain releases nothing.
    expect(released()).toEqual([])
  })

  it('calls onError when drainChannel runs with error termination', () => {
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

  it('keeps draining in drainChannel when a stream listener throws', () => {
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

  it('drops pending and stream together in unregisterRequest, and releases each id', () => {
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
    const { mux, released } = makeMux()
    mux.unregisterRequest(ch, 1)
    mux.unregisterRequest(ch, 2)
    expect(ch.pendingRequests.has(1)).toBe(false)
    expect(ch.streamListeners.has(2)).toBe(false)
    expect(released()).toEqual([1, 2])
  })

  it('wraps a non-ChannelError and unregisters in trySend', () => {
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

  it('rejects a pending request with the source and the message in rejectPendingRequest, and releases its id', async () => {
    const ch = makeChannel()
    const { mux, released } = makeMux()
    const pending = mux.call(ch, 'Big', new Uint8Array())

    mux.rejectPendingRequest(ch, 1, 'client', 'chunked message too large: 60 bytes exceeds 50 byte limit')

    await expect(pending).rejects.toMatchObject({ source: 'client', message: 'chunked message too large: 60 bytes exceeds 50 byte limit' })
    expect(ch.pendingRequests.size).toBe(0)
    expect(released()).toEqual([1])
  })

  it('fails a stream listener in rejectPendingRequest', () => {
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

  it('fail reports the error once and silences every listener', () => {
    const { mux } = makeMux()
    const ch = makeChannel()
    const handle = mux.beginStream(ch, 'S', new Uint8Array())
    const onMessage = vi.fn()
    const onEnd = vi.fn()
    const onError = vi.fn()
    handle.onMessage(onMessage)
    handle.onEnd(onEnd)
    handle.onError(onError)
    const failure = new ChannelError('client', 'bad frame')
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})

    handle.fail(failure, 'unused')
    mux.deliverStream(ch, handle.requestId, create(InnerStreamMessageSchema, {
      payload: new Uint8Array([1]),
    }))
    handle.fail(new ChannelError('client', 'a later failure'), 'a later failure with no onError listener')
    mux.deliverStream(ch, handle.requestId, create(InnerStreamMessageSchema, { end: true }))
    warnSpy.mockRestore()

    expect(onError).toHaveBeenCalledExactlyOnceWith(failure)
    expect(onMessage).not.toHaveBeenCalled()
    expect(onEnd).not.toHaveBeenCalled()
  })

  it('fail silences the listeners although the error listener throws', () => {
    const { mux } = makeMux()
    const ch = makeChannel()
    const handle = mux.beginStream(ch, 'S', new Uint8Array())
    const onEnd = vi.fn()
    const onError = vi.fn(() => {
      throw new Error('listener bug')
    })
    handle.onEnd(onEnd)
    handle.onError(onError)

    expect(() => handle.fail(new ChannelError('client', 'bad frame'), 'unused')).toThrow('listener bug')
    mux.deliverStream(ch, handle.requestId, create(InnerStreamMessageSchema, { end: true }))

    expect(onError).toHaveBeenCalledOnce()
    expect(onEnd).not.toHaveBeenCalled()
  })

  it('encodes a stream cancel as the cancel action alone', () => {
    expect(decodeStreamAction(buildStreamRequestPlaintext({ case: 'cancel' })).case).toBe('cancel')
  })

  it('encodes a stream revision as the payload action', () => {
    const payload = new Uint8Array([1, 2])
    const action = decodeStreamAction(buildStreamRequestPlaintext({ case: 'payload', payload }))
    expect(action.case).toBe('payload')
    expect(action.value).toEqual(payload)
  })

  // A WatchEventsRequest that watches nothing encodes to zero bytes. The frame
  // must still select the payload action on the wire, or the worker reads a
  // frame that selects no action and fails the stream.
  it('keeps an empty stream revision on the wire', () => {
    const action = decodeStreamAction(buildStreamRequestPlaintext({
      case: 'payload',
      payload: new Uint8Array(),
    }))
    expect(action.case).toBe('payload')
    expect(action.value).toEqual(new Uint8Array())
  })

  it('reuses the stream correlation id in sendOnStream, never as a starting send, and does not advance nextRequestId', () => {
    const ch = makeChannel({ nextRequestId: 5 })
    ch.streamListeners.set(3, {
      onMessage: () => {},
      onEnd: () => {},
      onError: () => {},
    })
    const { mux, sends, released } = makeMux()
    const err = mux.sendOnStream(ch, 3, {
      case: 'payload',
      payload: new Uint8Array([7]),
    })
    expect(err).toBeNull()
    expect(sends()).toHaveLength(1)
    expect(sends()[0]).toMatchObject({ requestId: 3, startsRequest: false })
    expect(released()).toEqual([])
    expect(ch.nextRequestId).toBe(5)
  })

  // The session holds a send while a rekey is due. The release must reach it
  // first, so that a cancel of a stream whose opening send still waits never
  // leaves the session, and the cancel itself must not make the id live again.
  it('releases the id before it sends a cancel, and sends the cancel as a follow-up', () => {
    const ch = makeChannel()
    ch.streamListeners.set(2, {
      onMessage: () => {},
      onEnd: () => {},
      onError: () => {},
    })
    const { mux, calls } = makeMux()
    mux.sendOnStream(ch, 2, { case: 'cancel' })
    expect(ch.streamListeners.has(2)).toBe(false)
    expect(calls.map(call => call.op === 'send'
      ? { op: call.op, id: call.requestId, startsRequest: call.startsRequest }
      : { op: call.op, id: call.correlationId }))
      .toEqual([
        { op: 'release', id: 2 },
        { op: 'send', id: 2, startsRequest: false },
      ])
  })

  it('returns an error from sendOnStream on an unregistered id', () => {
    const ch = makeChannel()
    const { mux, sends } = makeMux()
    const err = mux.sendOnStream(ch, 99, {
      case: 'payload',
      payload: new Uint8Array([1]),
    })
    expect(err).not.toBeNull()
    expect(err?.message).toMatch(/not registered/)
    expect(sends()).toHaveLength(0)
  })
})
