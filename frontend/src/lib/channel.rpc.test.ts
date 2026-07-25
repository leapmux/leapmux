import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ChannelMessageSchema,
  InnerMessageSchema,
  InnerRpcRequestSchema,
  InnerRpcResponseSchema,
} from '~/generated/leapmux/v1/channel_pb'
import { ChannelManager } from './channel'
import {
  channelInternals,
  ChannelManagerTestHarness,
  createMockTransport,
  decodeWireMessage,
  encodeCloseMessage,
  encodeWireMessage,
  encodeWireMessageWithBigIntId,
  FIRST_TEST_REQUEST_ID,
  mockHandshake1,
  mockHandshake2,
  MockWebSocket,
  sessions,
} from './channel.test-support'

describe('channelManager call', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  // Dropping a message must not desync the session.
  //
  // correlation_id is uint64 on the wire but a plain number in this client, so the
  // decode boundary refuses an id past the exact-integer range rather than rounding
  // it onto some other request's handler. That refusal is defensive -- a rounded id
  // is >= 2^53 and could only collide with an allocated id after ~450,000 years of
  // saturated traffic -- but WHERE it happens is not: Noise nonces are implicit and
  // sequential, so returning before the decrypt leaves this side's receive nonce
  // behind the peer's send nonce and every later message fails to decrypt.
  //
  // That is what this pins: the second half (a normal response still routes after
  // the drop) fails if the check is hoisted above the decrypt.
  it('stays usable after dropping a message with an out-of-range correlation id', async () => {
    const channelId = await h.openTestChannel('w1')
    const callPromise = h.mgr.call(channelId, 'TestMethod', new Uint8Array())
    const pair = sessions.get(channelId)!

    // 2^53 + 1 is not exactly representable and rounds DOWN onto 2^53, so a naive
    // Number() conversion would hand it to a live handler.
    const unsafeId = BigInt(Number.MAX_SAFE_INTEGER) + 1n
    const resp = create(InnerRpcResponseSchema, { payload: new Uint8Array([9]) })
    const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
    const ct = pair.responder.send.encrypt(toBinary(InnerMessageSchema, envelope))
    h.mockWs.simulateMessage(encodeWireMessageWithBigIntId(channelId, ct, unsafeId))

    // The pending call is untouched: not resolved, not rejected.
    const settled = await Promise.race([
      callPromise.then(() => 'settled' as const, () => 'settled' as const),
      new Promise<'pending'>(resolve => setTimeout(resolve, 20, 'pending')),
    ])
    expect(settled).toBe('pending')

    // ...and the real response still routes.
    h.sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([4, 5, 6]))
    await expect(callPromise).resolves.toMatchObject({ payload: new Uint8Array([4, 5, 6]) })
  })

  it('should send a request and receive a response', async () => {
    const channelId = await h.openTestChannel('w1')
    const callPromise = h.mgr.call(channelId, 'TestMethod', new Uint8Array([1, 2, 3]))

    const requestSentIndex = h.mockWs.sent.length - 1

    // Decrypt and verify the sent message.
    const sentMsg = decodeWireMessage(h.mockWs.sent[requestSentIndex])
    expect(sentMsg.channelId).toBe(channelId)
    const pair = sessions.get(channelId)!
    const sentPlaintext = pair.responder.receive.decrypt(sentMsg.ciphertext)
    const sentEnvelope = fromBinary(InnerMessageSchema, sentPlaintext)
    expect(sentEnvelope.kind.case).toBe('request')
    const sentReq = fromBinary(InnerRpcRequestSchema, toBinary(InnerRpcRequestSchema, sentEnvelope.kind.value as any))
    expect(sentReq.method).toBe('TestMethod')
    expect(sentReq.payload).toEqual(new Uint8Array([1, 2, 3]))
    expect(Number(sentMsg.correlationId)).toBe(FIRST_TEST_REQUEST_ID)

    // Send a response from the worker.
    h.sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([4, 5, 6]))

    const resp = await callPromise
    expect(resp.payload).toEqual(new Uint8Array([4, 5, 6]))
  })

  it('should reject on error response', async () => {
    const channelId = await h.openTestChannel('w1')
    const callPromise = h.mgr.call(channelId, 'TestMethod', new Uint8Array())

    h.sendErrorResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, 'something went wrong')

    await expect(callPromise).rejects.toThrow('something went wrong')
  })

  it('should reject if channel is not open', async () => {
    await expect(h.mgr.call('nonexistent', 'Test', new Uint8Array())).rejects.toThrow('channel not open')
  })

  it('should reject if channel is closed', async () => {
    const channelId = await h.openTestChannel('w1')
    await h.mgr.closeChannel(channelId)
    await expect(h.mgr.call(channelId, 'Test', new Uint8Array())).rejects.toThrow('channel not open')
  })

  it('rejects promptly when the socket is not OPEN instead of hanging until the RPC timeout', async () => {
    const channelId = await h.openTestChannel('w1')
    // The socket goes to CLOSED WITHOUT its close event draining the channel
    // (a stale/superseded socket the current-socket fence dropped): the channel
    // is still 'verified', so a call reaches the send. sendChannelMessage must
    // THROW here rather than log-and-return, so call()'s catch unregisters and
    // rejects fast -- otherwise the request sits in pendingRequests until the
    // ~15s timeout. No fake timers are advanced, so this only resolves if the
    // rejection is immediate.
    h.mockWs.readyState = MockWebSocket.CLOSED
    await expect(h.mgr.call(channelId, 'Test', new Uint8Array([1]))).rejects.toThrow(/WebSocket not open/)
  })

  it('should handle multiple concurrent calls', async () => {
    const channelId = await h.openTestChannel('w1')

    const call1 = h.mgr.call(channelId, 'Method1', new Uint8Array([1]))
    const call2 = h.mgr.call(channelId, 'Method2', new Uint8Array([2]))

    // Respond in reverse order.
    h.sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID + 1, new Uint8Array([20]))
    h.sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([10]))

    const [resp1, resp2] = await Promise.all([call1, call2])
    expect(resp1.payload).toEqual(new Uint8Array([10]))
    expect(resp2.payload).toEqual(new Uint8Array([20]))
  })

  it('should timeout after default rpcTimeout (15s)', async () => {
    vi.useFakeTimers()
    try {
      const channelId = await h.openTestChannel('w1')
      const callPromise = h.mgr.call(channelId, 'SlowMethod', new Uint8Array())

      vi.advanceTimersByTime(15_000)

      await expect(callPromise).rejects.toThrow('timed out after 15s')
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('should honor a per-call timeout override', async () => {
    vi.useFakeTimers()
    try {
      const channelId = await h.openTestChannel('w1')
      const callPromise = h.mgr.call(channelId, 'SlowMethod', new Uint8Array(), 40_000)

      vi.advanceTimersByTime(39_999)
      await Promise.resolve()

      vi.advanceTimersByTime(1)
      await expect(callPromise).rejects.toThrow('timed out after 40s')
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('rejects immediately when an already-aborted signal is passed', async () => {
    const channelId = await h.openTestChannel('w1')
    const controller = new AbortController()
    controller.abort(new Error('pre-aborted by caller'))
    const callPromise = h.mgr.call(channelId, 'TestMethod', new Uint8Array(), undefined, controller.signal)
    await expect(callPromise).rejects.toThrow('pre-aborted by caller')
  })

  it('rejects the pending promise when the signal aborts mid-flight', async () => {
    const channelId = await h.openTestChannel('w1')
    const controller = new AbortController()
    const callPromise = h.mgr.call(channelId, 'TestMethod', new Uint8Array(), undefined, controller.signal)
    controller.abort(new Error('caller dismissed the dialog'))
    await expect(callPromise).rejects.toThrow('caller dismissed the dialog')
  })

  it('drops the pending entry on abort so a late InnerRpcResponse is harmless', async () => {
    const channelId = await h.openTestChannel('w1')
    const controller = new AbortController()
    const callPromise = h.mgr.call(channelId, 'TestMethod', new Uint8Array(), undefined, controller.signal)
    controller.abort(new Error('aborted'))
    await expect(callPromise).rejects.toThrow('aborted')
    // A late worker response for the same correlationId must NOT
    // throw, double-resolve, or surface as an unhandled rejection.
    // The pendingRequest entry was deleted at abort time, so the
    // dispatcher quietly drops the message.
    h.sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([7, 8, 9]))
    // No assertion needed beyond "doesn't crash" — vitest fails
    // the test on unhandled rejections from the now-detached
    // promise, so the absence of those failures is the signal.
  })

  it('clears the timeout timer when aborted so it cannot fire later and double-reject', async () => {
    vi.useFakeTimers()
    try {
      const channelId = await h.openTestChannel('w1')
      const controller = new AbortController()
      const callPromise = h.mgr.call(channelId, 'TestMethod', new Uint8Array(), undefined, controller.signal)
      controller.abort(new Error('aborted'))
      await expect(callPromise).rejects.toThrow('aborted')
      // Advance past the default timeout to prove the timer was
      // cleared — without cleanup, vitest's unhandled-rejection
      // detector would fire when the orphan timer rejected a
      // settled promise.
      vi.advanceTimersByTime(20_000)
    }
    finally {
      vi.useRealTimers()
    }
  })
})

describe('channelManager stream', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should receive stream messages', async () => {
    const channelId = await h.openTestChannel('w1')
    const messages: Uint8Array[] = []
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onMessage((msg) => {
      messages.push(msg.payload)
    })

    h.sendStreamMessageFromWorker(channelId, handle.requestId, new Uint8Array([1]))
    h.sendStreamMessageFromWorker(channelId, handle.requestId, new Uint8Array([2]))

    expect(messages).toHaveLength(2)
    expect(messages[0]).toEqual(new Uint8Array([1]))
    expect(messages[1]).toEqual(new Uint8Array([2]))
  })

  it('should handle stream end', async () => {
    const channelId = await h.openTestChannel('w1')
    const endFn = vi.fn()
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onEnd(endFn)

    h.sendStreamEndFromWorker(channelId, handle.requestId)

    expect(endFn).toHaveBeenCalledOnce()
  })

  it('should handle stream error', async () => {
    const channelId = await h.openTestChannel('w1')
    const errorFn = vi.fn()
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onError(errorFn)

    h.sendStreamErrorFromWorker(channelId, handle.requestId, 'stream broke')

    expect(errorFn).toHaveBeenCalledOnce()
    expect(errorFn.mock.calls[0][0].message).toBe('stream broke')
  })

  it('should error the stream when the worker replies with a unary error', async () => {
    // A streaming method whose error arrives as an InnerRpcResponse
    // rather than a stream frame: a gate rejection, a panic in a handler
    // registered as unary, or the dispatcher's Unimplemented reply for a
    // method it has no registration for at all -- which cannot know the
    // shape by construction. Dropping it left the subscription waiting
    // forever with nothing to retry from.
    const channelId = await h.openTestChannel('w1')
    const errorFn = vi.fn()
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onError(errorFn)

    h.sendErrorResponseFromWorker(channelId, handle.requestId, 'workspace not accessible')

    expect(errorFn).toHaveBeenCalledOnce()
    expect(errorFn.mock.calls[0][0].message).toBe('workspace not accessible')
    expect(channelInternals(h.mgr, channelId).streamListeners.has(handle.requestId)).toBe(false)
  })

  it('should ignore a non-error unary reply on a stream id', async () => {
    // The inverse guard: a success payload on a stream id is not
    // something a listener can interpret, so dropping it stays correct
    // and must not be mistaken for an error.
    const channelId = await h.openTestChannel('w1')
    const errorFn = vi.fn()
    const endFn = vi.fn()
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onError(errorFn)
    handle.onEnd(endFn)

    h.sendResponseFromWorker(channelId, handle.requestId, new Uint8Array([7]))

    expect(errorFn).not.toHaveBeenCalled()
    expect(endFn).not.toHaveBeenCalled()
    expect(channelInternals(h.mgr, channelId).streamListeners.has(handle.requestId)).toBe(true)
  })

  it('should throw if channel is not open', async () => {
    expect(() => h.mgr.stream('nonexistent', 'WatchEvents', new Uint8Array())).toThrow('channel not open')
  })

  it('surfaces a send failure (throws + unregisters) when the socket is not OPEN', async () => {
    const channelId = await h.openTestChannel('w1')
    // Grab the channel object up front: the throw's onSendFailure closes the
    // channel (a 'transport' error) and removes it from the manager's map, but
    // stream()'s catch unregisters the listener on THIS same object first.
    const internals = channelInternals(h.mgr, channelId)
    // The socket is CLOSED but the channel is still 'verified' (a stale socket
    // whose close event was dropped). stream()'s initial request send must
    // throw rather than be silently dropped, or the stream listener stays
    // registered producing no data and no error until the channel is torn down.
    h.mockWs.readyState = MockWebSocket.CLOSED
    expect(() => h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())).toThrow(/WebSocket not open/)
    // The catch unregistered the listener rather than leaking it.
    expect(internals.streamListeners.size).toBe(0)
  })

  it('should unregister the stream even when onError throws', async () => {
    const channelId = await h.openTestChannel('w1')
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onError(() => {
      throw new Error('listener bug')
    })

    // A throwing terminal callback must not skip unregisterRequest: before the
    // fix the throw propagated out of deliverStream, leaving the listener and
    // its reassembly slot registered forever (four of them exhaust
    // MAX_INCOMPLETE_CHUNKED and wedge the channel). It must also not unwind
    // into the WebSocket message dispatch.
    expect(() => h.sendStreamErrorFromWorker(channelId, handle.requestId, 'stream broke')).not.toThrow()
    expect(channelInternals(h.mgr, channelId).streamListeners.has(handle.requestId)).toBe(false)
  })

  it('should unregister the stream even when onEnd throws', async () => {
    const channelId = await h.openTestChannel('w1')
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onEnd(() => {
      throw new Error('listener bug')
    })

    expect(() => h.sendStreamEndFromWorker(channelId, handle.requestId)).not.toThrow()
    expect(channelInternals(h.mgr, channelId).streamListeners.has(handle.requestId)).toBe(false)
  })

  it('should keep the stream live and isolated when onMessage throws mid-stream', async () => {
    const channelId = await h.openTestChannel('w1')
    const received: Uint8Array[] = []
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    let calls = 0
    handle.onMessage((msg) => {
      calls++
      if (calls === 1)
        throw new Error('listener bug')
      received.push(msg.payload)
    })

    // A throwing per-chunk callback must be isolated (safeCall) so it neither
    // unwinds into the WS message dispatch nor stops later chunks arriving.
    expect(() => h.sendStreamMessageFromWorker(channelId, handle.requestId, new Uint8Array([1]))).not.toThrow()
    expect(() => h.sendStreamMessageFromWorker(channelId, handle.requestId, new Uint8Array([2]))).not.toThrow()
    expect(received).toEqual([new Uint8Array([2])])
    // Still live: a non-terminal throw does not unregister.
    expect(channelInternals(h.mgr, channelId).streamListeners.has(handle.requestId)).toBe(true)
  })
})

describe('channelManager message routing', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should ignore messages for unknown channels', async () => {
    await h.openTestChannel('w1')
    // Should not throw.
    h.mockWs.simulateMessage(encodeWireMessage('unknown-channel', new Uint8Array([1, 2, 3])))
  })

  it('should route messages to the correct channel', async () => {
    const ch1 = await h.openTestChannel('w1')
    const ch2 = await h.openTestChannel('w2')

    const call1 = h.mgr.call(ch1, 'M1', new Uint8Array())
    const call2 = h.mgr.call(ch2, 'M2', new Uint8Array())

    h.sendResponseFromWorker(ch2, FIRST_TEST_REQUEST_ID, new Uint8Array([20]))
    h.sendResponseFromWorker(ch1, FIRST_TEST_REQUEST_ID, new Uint8Array([10]))

    const [resp1, resp2] = await Promise.all([call1, call2])
    expect(resp1.payload).toEqual(new Uint8Array([10]))
    expect(resp2.payload).toEqual(new Uint8Array([20]))
  })
})

describe('channelManager chunking', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  /** Encode a wire message with specific flags. */
  function encodeWireMessageWithFlags(channelId: string, ciphertext: Uint8Array, opts: { correlationId: number, flags: number }): ArrayBuffer {
    const msg = create(ChannelMessageSchema, {
      protocolVersion: 1,
      channelId,
      ciphertext,
      correlationId: BigInt(opts.correlationId),
      flags: opts.flags,
    })
    const data = toBinary(ChannelMessageSchema, msg)
    const buf = new Uint8Array(4 + data.length)
    new DataView(buf.buffer).setUint32(0, data.length)
    buf.set(data, 4)
    return buf.buffer
  }

  it('should send a single chunk for small plaintext', async () => {
    const channelId = await h.openTestChannel('w1')
    const sentBefore = h.mockWs.sent.length

    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array([1, 2, 3]))

    const sentAfter = h.mockWs.sent.length
    expect(sentAfter - sentBefore).toBe(1) // Just 1 frame

    const msg = decodeWireMessage(h.mockWs.sent[sentAfter - 1])
    expect(msg.flags).toBe(0) // UNSPECIFIED

    // Complete the call so it doesn't stay pending during cleanup.
    h.sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([42]))
    await callPromise
  })

  it('should send multiple chunks for large plaintext', async () => {
    // Create a manager with a small max chunk awareness.
    // We can't easily test actual chunking without matching MAX_CHUNK_SIZE,
    // but we can verify the chunk splitting logic by checking the wire output.
    const channelId = await h.openTestChannel('w1')
    const sentBefore = h.mockWs.sent.length

    // Send a request — the payload itself is small enough for one chunk.
    // This just validates the normal path works.
    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array(10))
    const sentAfter = h.mockWs.sent.length
    expect(sentAfter - sentBefore).toBe(1)

    // Complete the call.
    h.sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([42]))
    const resp = await callPromise
    expect(resp.payload).toEqual(new Uint8Array([42]))
  })

  it('should reassemble multi-chunk response', async () => {
    const channelId = await h.openTestChannel('w1')
    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array())

    const pair = sessions.get(channelId)!

    // Build a response InnerMessage.
    const resp = create(InnerRpcResponseSchema, {
      payload: new Uint8Array([10, 20, 30, 40, 50]),
      isError: false,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'response', value: resp },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)

    // Split the plaintext into 2 chunks (simulate chunking).
    const mid = Math.floor(plaintext.length / 2)
    const chunk1 = plaintext.slice(0, mid)
    const chunk2 = plaintext.slice(mid)

    // Encrypt each chunk separately.
    const ct1 = pair.responder.send.encrypt(chunk1)
    const ct2 = pair.responder.send.encrypt(chunk2)

    // Send chunk1 with flags=MORE.
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct1, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

    // Send chunk2 with flags=UNSPECIFIED (final).
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct2, { correlationId: FIRST_TEST_REQUEST_ID, flags: 0 }))

    const result = await callPromise
    expect(result.payload).toEqual(new Uint8Array([10, 20, 30, 40, 50]))
  })

  // An out-of-spec flags value (e.g. MORE|CLOSE combined, which no conformant
  // sender emits) must be dropped -- not read as "final chunk" and delivered
  // truncated -- and the drop must come AFTER the decrypt so the receive
  // nonce stays in step with the peer (mirrors the Go receivers'
  // channelwire.ChunkContinuation).
  it('drops a frame with out-of-spec flags without desyncing the receive nonce', async () => {
    const channelId = await h.openTestChannel('w1')
    const pair = sessions.get(channelId)!
    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array([1]))

    const resp = create(InnerRpcResponseSchema, { payload: new Uint8Array([9]), isError: false })
    const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
    const ct1 = pair.responder.send.encrypt(toBinary(InnerMessageSchema, envelope))
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct1, { correlationId: FIRST_TEST_REQUEST_ID, flags: 3 }))

    // The dropped frame advanced the receive nonce: the peer's NEXT
    // ciphertext still decrypts and resolves the call.
    h.sendResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, new Uint8Array([42]))
    const result = await callPromise
    expect(result.payload).toEqual(new Uint8Array([42]))
  })

  it('should drop oversized chunked messages', async () => {
    // Create a manager with a very small max message size.
    sessions.clear()
    const smallMgr = new ChannelManager(createMockTransport(h.mockWs, { maxMessageSize: false }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      testPayloadBudget: 50,
      testReassembledCeiling: 50,
    })

    const openPromise = smallMgr.openChannel('w1')
    await h.flushMicrotasks()
    h.mockWs.simulateOpen()
    await h.flushMicrotasks()
    h.simulatePingAccept()

    const channelId = await openPromise
    const pair = sessions.get(channelId)!

    const callPromise = smallMgr.call(channelId, 'Test', new Uint8Array())

    // Send a chunk that's within limits.
    const chunk1Data = new Uint8Array(30)
    const ct1 = pair.responder.send.encrypt(chunk1Data)
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct1, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

    // Send another chunk that exceeds the 50-byte limit (total 60 > 50).
    const chunk2Data = new Uint8Array(30)
    const ct2 = pair.responder.send.encrypt(chunk2Data)
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct2, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

    // The call should be rejected with an error about the size limit.
    await expect(callPromise).rejects.toThrow('exceeds')

    smallMgr.closeAll()
  })

  // The size limit must hold when the FINAL chunk is what breaches it, not just
  // when a MORE chunk does. A peer chooses its own framing, so a limit enforced on
  // only one of the two paths is not a limit on the message at all -- it just
  // moves the bypass to the other framing.
  it('should drop a chunked message whose final chunk exceeds the limit', async () => {
    sessions.clear()
    const smallMgr = new ChannelManager(createMockTransport(h.mockWs, { maxMessageSize: false }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      testPayloadBudget: 50,
      testReassembledCeiling: 50,
    })

    const openPromise = smallMgr.openChannel('w1')
    await h.flushMicrotasks()
    h.mockWs.simulateOpen()
    await h.flushMicrotasks()
    h.simulatePingAccept()

    const channelId = await openPromise
    const pair = sessions.get(channelId)!

    const callPromise = smallMgr.call(channelId, 'Test', new Uint8Array())

    // A MORE chunk within limits.
    const ct1 = pair.responder.send.encrypt(new Uint8Array(30))
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct1, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

    // The FINAL chunk (flags: 0) pushes the total to 60 > 50.
    const ct2 = pair.responder.send.encrypt(new Uint8Array(30))
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct2, { correlationId: FIRST_TEST_REQUEST_ID, flags: 0 }))

    await expect(callPromise).rejects.toThrow('exceeds')

    smallMgr.closeAll()
  })

  it('should reject too many incomplete chunked sequences', async () => {
    const channelId = await h.openTestChannel('w1')
    const pair = sessions.get(channelId)!

    // Start MAX_INCOMPLETE_CHUNKED (4) chunked sequences.
    for (let i = 1; i <= 4; i++) {
      const chunk = new Uint8Array([i])
      const ct = pair.responder.send.encrypt(chunk)
      h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct, { correlationId: i, flags: 1 }))
    }

    // 5th should be dropped (exceeded MAX_INCOMPLETE_CHUNKED).
    const chunk5 = new Uint8Array([5])
    const ct5 = pair.responder.send.encrypt(chunk5)
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct5, { correlationId: 5, flags: 1 }))

    // Channel should still be functional — close notification works.
    h.mockWs.simulateMessage(encodeCloseMessage(channelId))
    expect(h.mgr.isOpen(channelId)).toBe(false)
  })

  it('should throw on send when plaintext exceeds maxMessageSize', async () => {
    sessions.clear()
    const smallMgr = new ChannelManager(createMockTransport(h.mockWs, { maxMessageSize: false }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      testPayloadBudget: 50,
      testReassembledCeiling: 50,
    })

    const openPromise = smallMgr.openChannel('w1')
    await h.flushMicrotasks()
    h.mockWs.simulateOpen()
    await h.flushMicrotasks()
    h.simulatePingAccept()

    const channelId = await openPromise

    // A large payload should cause call() to reject with "message too large".
    await expect(smallMgr.call(channelId, 'Test', new Uint8Array(200))).rejects.toThrow('message too large')

    smallMgr.closeAll()
  })

  it('should reject on final chunk oversize', async () => {
    sessions.clear()
    const smallMgr = new ChannelManager(createMockTransport(h.mockWs, { maxMessageSize: false }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      testPayloadBudget: 50,
      testReassembledCeiling: 50,
    })

    const openPromise = smallMgr.openChannel('w1')
    await h.flushMicrotasks()
    h.mockWs.simulateOpen()
    await h.flushMicrotasks()
    h.simulatePingAccept()

    const channelId = await openPromise
    const pair = sessions.get(channelId)!

    const callPromise = smallMgr.call(channelId, 'Test', new Uint8Array())

    // Send a first chunk within limits (30 bytes).
    const chunk1 = pair.responder.send.encrypt(new Uint8Array(30))
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk1, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

    // Send a final chunk that pushes over (30 + 30 = 60 > 50).
    const chunk2 = pair.responder.send.encrypt(new Uint8Array(30))
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk2, { correlationId: FIRST_TEST_REQUEST_ID, flags: 0 }))

    await expect(callPromise).rejects.toThrow('exceeds')

    smallMgr.closeAll()
  })

  it('should route chunking errors to stream listeners', async () => {
    sessions.clear()
    const smallMgr = new ChannelManager(createMockTransport(h.mockWs, { maxMessageSize: false }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      testPayloadBudget: 50,
      testReassembledCeiling: 50,
    })

    const openPromise = smallMgr.openChannel('w1')
    await h.flushMicrotasks()
    h.mockWs.simulateOpen()
    await h.flushMicrotasks()
    h.simulatePingAccept()

    const channelId = await openPromise
    const pair = sessions.get(channelId)!

    const errorFn = vi.fn()
    const handle = smallMgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onError(errorFn)

    // Send chunks that exceed the limit, targeted at the stream's requestId.
    const chunk1 = pair.responder.send.encrypt(new Uint8Array(30))
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk1, { correlationId: handle.requestId, flags: 1 }))

    const chunk2 = pair.responder.send.encrypt(new Uint8Array(30))
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk2, { correlationId: handle.requestId, flags: 1 }))

    expect(errorFn).toHaveBeenCalledOnce()
    expect(errorFn.mock.calls[0][0].message).toContain('exceeds')

    smallMgr.closeAll()
  })

  it('should clear reassembly on close', async () => {
    const channelId = await h.openTestChannel('w1')
    const pair = sessions.get(channelId)!

    // Start a chunked sequence.
    const chunk = new Uint8Array([1, 2, 3])
    const ct = pair.responder.send.encrypt(chunk)
    h.mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct, { correlationId: FIRST_TEST_REQUEST_ID, flags: 1 }))

    // Close the channel — should not crash and should clean up.
    await h.mgr.closeChannel(channelId)
    expect(h.mgr.isOpen(channelId)).toBe(false)
  })

  /** Open a channel on a manager sharing the suite's h.mockWs (already connected after the first open). */
  async function openOn(cm: ChannelManager, workerId = 'w1'): Promise<string> {
    const openPromise = cm.openChannel(workerId)
    await h.flushMicrotasks()
    if (h.mockWs.readyState !== MockWebSocket.OPEN) {
      h.mockWs.simulateOpen()
      await h.flushMicrotasks()
    }
    h.simulatePingAccept()
    return openPromise
  }

  // A payload the client refuses to send must not leave its bookkeeping behind.
  // call() installs the pending entry and the timeout timer BEFORE the send, inside
  // the Promise executor -- and a throw from an executor rejects the promise without
  // unwinding anything it had set up.
  it('leaves no pending entry behind when a payload is too large to send', async () => {
    sessions.clear()
    const smallMgr = new ChannelManager(createMockTransport(h.mockWs, { maxMessageSize: false }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      testPayloadBudget: 50,
      testReassembledCeiling: 50,
    })
    try {
      const channelId = await openOn(smallMgr)

      await expect(smallMgr.call(channelId, 'Test', new Uint8Array(200))).rejects.toThrow('message too large')

      expect(channelInternals(smallMgr, channelId).pendingRequests.size).toBe(0)
    }
    finally {
      smallMgr.closeAll()
    }
  })

  // stream() is the worse half: the throw escapes BEFORE the handle is returned, so
  // the caller never learns the requestId and can never removeStreamListener. The
  // entry would live as long as the channel.
  it('leaves no stream listener behind when a payload is too large to send', async () => {
    sessions.clear()
    const smallMgr = new ChannelManager(createMockTransport(h.mockWs, { maxMessageSize: false }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      testPayloadBudget: 50,
      testReassembledCeiling: 50,
    })
    try {
      const channelId = await openOn(smallMgr)

      expect(() => smallMgr.stream(channelId, 'WatchEvents', new Uint8Array(200))).toThrow('message too large')

      expect(channelInternals(smallMgr, channelId).streamListeners.size).toBe(0)
    }
    finally {
      smallMgr.closeAll()
    }
  })

  // One caller's oversized payload must not cost every other caller the channel: the
  // session never encrypted a byte, so it is still perfectly good.
  it('keeps the channel usable after refusing an oversized payload', async () => {
    sessions.clear()
    const smallMgr = new ChannelManager(createMockTransport(h.mockWs, { maxMessageSize: false }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      testPayloadBudget: 50,
      testReassembledCeiling: 50,
    })
    try {
      const channelId = await openOn(smallMgr)
      await expect(smallMgr.call(channelId, 'Test', new Uint8Array(200))).rejects.toThrow('message too large')

      expect(smallMgr.isOpen(channelId)).toBe(true)
      // The next call goes out on the same channel and round-trips normally.
      const callPromise = smallMgr.call(channelId, 'Test', new Uint8Array([7]))
      const sentMsg = decodeWireMessage(h.mockWs.sent.at(-1)!)
      const pair = sessions.get(channelId)!
      pair.responder.receive.decrypt(sentMsg.ciphertext)
      h.sendResponseFromWorker(channelId, Number(sentMsg.correlationId), new Uint8Array([42]))
      await expect(callPromise).resolves.toMatchObject({ payload: new Uint8Array([42]) })
      expect(await smallMgr.getOrOpenChannel('w1')).toBe(channelId)
    }
    finally {
      smallMgr.closeAll()
    }
  })

  // An encrypt failure is the opposite case: the Noise send state is finished (nonce
  // ceiling) or a chunked send left the peer's receive nonce ahead of ours, so every
  // later send on this channel is garbage. Left in the pool, getOrOpenChannel would
  // hand it to every later caller and each would fail identically for up to an hour.
  it('closes the channel when encrypting a call fails, so pooled callers re-resolve', async () => {
    const channelId = await h.openTestChannel('w1')
    channelInternals(h.mgr, channelId).session.send.encrypt = () => {
      throw new Error('noise: nonce overflow (hard limit)')
    }

    await expect(h.mgr.call(channelId, 'Test', new Uint8Array([1]))).rejects.toThrow('nonce overflow')
    expect(h.mgr.isOpen(channelId)).toBe(false)
    expect(h.mgr.hasOpenChannel('w1')).toBe(false)

    // The pooled caller gets a NEW channel rather than the dead one.
    const nextPromise = h.mgr.getOrOpenChannel('w1')
    await h.flushMicrotasks()
    h.simulatePingAccept()
    const nextId = await nextPromise
    expect(nextId).not.toBe(channelId)
    expect(h.mgr.isOpen(nextId)).toBe(true)
  })

  it('closes the channel when encrypting a stream request fails', async () => {
    const channelId = await h.openTestChannel('w1')
    channelInternals(h.mgr, channelId).session.send.encrypt = () => {
      throw new Error('noise: nonce overflow (hard limit)')
    }

    expect(() => h.mgr.stream(channelId, 'WatchEvents', new Uint8Array([1]))).toThrow('nonce overflow')
    expect(h.mgr.isOpen(channelId)).toBe(false)
  })
})

describe('channelManager reassembly lifetime', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  function encodeChunk(channelId: string, ciphertext: Uint8Array, correlationId: number, more: boolean): ArrayBuffer {
    const msg = create(ChannelMessageSchema, {
      protocolVersion: 1,
      channelId,
      ciphertext,
      correlationId: BigInt(correlationId),
      flags: more ? 1 : 0,
    })
    const data = toBinary(ChannelMessageSchema, msg)
    const buf = new Uint8Array(4 + data.length)
    new DataView(buf.buffer).setUint32(0, data.length)
    buf.set(data, 4)
    return buf.buffer
  }

  async function openSmallMgr(maxMessageSize: number, rpcTimeoutMs?: number): Promise<{ cm: ChannelManager, channelId: string }> {
    sessions.clear()
    const cm = new ChannelManager(createMockTransport(h.mockWs, { maxMessageSize: false }), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      testPayloadBudget: maxMessageSize,
      testReassembledCeiling: maxMessageSize,
      ...(rpcTimeoutMs === undefined ? {} : { rpcTimeoutFn: () => rpcTimeoutMs }),
    })
    const openPromise = cm.openChannel('w1')
    await h.flushMicrotasks()
    if (h.mockWs.readyState !== MockWebSocket.OPEN) {
      h.mockWs.simulateOpen()
      await h.flushMicrotasks()
    }
    h.simulatePingAccept()
    return { cm, channelId: await openPromise }
  }

  // (a) A breach must POISON the id, not delete its buffer. Deleting it erased the
  // only record that the id had failed: the next chunk found no buffer, passed the
  // cap check (the deleted bytes no longer counted), allocated a fresh one and let
  // the peer re-accumulate to the limit -- silently, since the breach had already
  // unregistered the handler. That cycle repeats for as long as the peer keeps
  // sending.
  it('drops the rest of a chunked message after it breaches the size limit', async () => {
    const { cm, channelId } = await openSmallMgr(50)
    try {
      const pair = sessions.get(channelId)!
      const errorFn = vi.fn()
      const handle = cm.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onError(errorFn)

      // 30 + 30 > 50: the breach.
      h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
      h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
      expect(errorFn).toHaveBeenCalledOnce()

      // The peer keeps shovelling. Not one byte may be buffered, and the failure is
      // not re-reported.
      for (let i = 0; i < 200; i++) {
        h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
      }
      expect(errorFn).toHaveBeenCalledOnce()

      const ch = channelInternals(cm, channelId)
      const tombstone = ch.reassembly.get(handle.requestId)
      expect(tombstone).toBeDefined()
      expect(tombstone!.poisoned).toBe(true)
      expect(tombstone!.parts).toHaveLength(0)
      expect(tombstone!.total).toBe(0)
      // A tombstone holds no bytes, so it must not hold a cap slot either.
      expect(ch.reassembly.liveCount()).toBe(0)

      // The message's final chunk reaps the tombstone.
      h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, false))
      expect(ch.reassembly.size()).toBe(0)
      expect(errorFn).toHaveBeenCalledOnce()
    }
    finally {
      cm.closeAll()
    }
  })

  // A throwing stream onError must NOT skip the reassembly poison that follows it.
  // failReassembly reports the breach (invoking the app's onError) and THEN
  // tombstones the id. Before safeCall wrapped that onError call, a throw unwound
  // out of failReassembly -> reassemble -> handleMessage, so poison never ran: the
  // id was left un-tombstoned (its buffer already reaped), and every later chunk of
  // the oversize message re-entered the unknown-id warn path -- the per-chunk storm
  // the tombstone exists to prevent.
  it('still poisons a breached id when the stream onError throws', async () => {
    const { cm, channelId } = await openSmallMgr(50)
    try {
      const pair = sessions.get(channelId)!
      let errorCalls = 0
      const handle = cm.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onError(() => {
        errorCalls++
        throw new Error('listener boom')
      })

      // 30 + 30 > 50: the breach. The throwing onError must not escape the message
      // dispatch.
      h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
      expect(() => {
        h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
      }).not.toThrow()
      expect(errorCalls).toBe(1)

      // The id is tombstoned despite the throw, so the remaining chunks are dropped
      // silently rather than re-accumulating and re-reporting.
      const ch = channelInternals(cm, channelId)
      expect(ch.reassembly.get(handle.requestId)?.poisoned).toBe(true)
      expect(ch.reassembly.liveCount()).toBe(0)

      for (let i = 0; i < 50; i++)
        h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
      expect(errorCalls).toBe(1)
    }
    finally {
      cm.closeAll()
    }
  })

  // (b) Every inbound chunked message answers a request THIS side registered, so a
  // first chunk for an id with no live handler can never complete. Buffering it would
  // pin up to maxMessageSize forever, and four such orphans would exhaust the cap and
  // permanently reject every later chunked message on a healthy channel.
  it('drops a chunk for an unknown correlation id without consuming a cap slot', async () => {
    const channelId = await h.openTestChannel('w1')
    const pair = sessions.get(channelId)!

    for (let id = 100; id < 104; id++) {
      h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), id, true))
    }

    const ch = channelInternals(h.mgr, channelId)
    expect(ch.reassembly.size()).toBe(0)
    expect(ch.reassembly.liveCount()).toBe(0)

    // The cap is untouched: a real chunked response still reassembles.
    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array())
    const resp = create(InnerRpcResponseSchema, { payload: new Uint8Array([9, 9]), isError: false })
    const envelope = create(InnerMessageSchema, { kind: { case: 'response', value: resp } })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const mid = Math.floor(plaintext.length / 2)
    h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(plaintext.slice(0, mid)), FIRST_TEST_REQUEST_ID, true))
    h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(plaintext.slice(mid)), FIRST_TEST_REQUEST_ID, false))

    await expect(callPromise).resolves.toMatchObject({ payload: new Uint8Array([9, 9]) })
  })

  // (c) A reassembly buffer exists only to feed one request, so it must die with it.
  // The timeout drops the handler; nothing else would ever come back for the bytes.
  it('reaps the reassembly buffer when its request times out', async () => {
    const { cm, channelId } = await openSmallMgr(1024, 20)
    try {
      const pair = sessions.get(channelId)!
      const callPromise = cm.call(channelId, 'Test', new Uint8Array())

      h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), FIRST_TEST_REQUEST_ID, true))
      const ch = channelInternals(cm, channelId)
      expect(ch.reassembly.size()).toBe(1)
      expect(ch.reassembly.liveCount()).toBe(1)

      await expect(callPromise).rejects.toThrow('timed out')

      expect(ch.reassembly.size()).toBe(0)
      expect(ch.reassembly.liveCount()).toBe(0)
    }
    finally {
      cm.closeAll()
    }
  })

  it('reaps the reassembly buffer when a stream listener is removed', async () => {
    const channelId = await h.openTestChannel('w1')
    const pair = sessions.get(channelId)!
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())

    h.mockWs.simulateMessage(encodeChunk(channelId, pair.responder.send.encrypt(new Uint8Array(30)), handle.requestId, true))
    const ch = channelInternals(h.mgr, channelId)
    expect(ch.reassembly.size()).toBe(1)

    h.mgr.removeStreamListener(channelId, handle.requestId)
    expect(ch.reassembly.size()).toBe(0)
    expect(ch.reassembly.liveCount()).toBe(0)
  })
})

describe('channelManager observability hooks onChannelError', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should fire on RPC error (non-transport)', async () => {
    const channelId = await h.openTestChannel('w1')
    const cb = vi.fn()
    h.mgr.onChannelError(cb)

    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array())
    h.sendErrorResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, 'rpc failed')
    await expect(callPromise).rejects.toThrow('rpc failed')

    expect(cb).toHaveBeenCalledOnce()
    expect(cb.mock.calls[0][0]).toBe('w1')
    expect(cb.mock.calls[0][1].source).toBe('rpc')
  })

  it('should fire on stream error (non-transport)', async () => {
    const channelId = await h.openTestChannel('w1')
    const cb = vi.fn()
    h.mgr.onChannelError(cb)

    const errorFn = vi.fn()
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onError(errorFn)

    h.sendStreamErrorFromWorker(channelId, handle.requestId, 'stream broke')

    expect(cb).toHaveBeenCalledOnce()
    expect(cb.mock.calls[0][0]).toBe('w1')
    expect(cb.mock.calls[0][1].source).toBe('stream')
  })

  it('should not fire after unsubscribe', async () => {
    const channelId = await h.openTestChannel('w1')
    const cb = vi.fn()
    const unsub = h.mgr.onChannelError(cb)
    unsub()

    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array())
    h.sendErrorResponseFromWorker(channelId, FIRST_TEST_REQUEST_ID, 'rpc failed')
    await expect(callPromise).rejects.toThrow('rpc failed')

    expect(cb).not.toHaveBeenCalled()
  })
})
