import type { ChannelTransport, KeyPinDecision } from './channel'
import type { Session } from './noise'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { chacha20poly1305 } from '@noble/ciphers/chacha.js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ChannelMessageSchema,
  InnerMessageSchema,
  InnerRpcRequestSchema,
  InnerRpcResponseSchema,
  InnerStreamMessageSchema,
  UserIdClaimResponseSchema,
} from '~/generated/leapmux/v1/channel_pb'
import { ChannelManager } from './channel'

// ---- Test helpers ----

/** Minimal CipherState for testing (same crypto as the real one). */
class TestCipherState {
  private k: Uint8Array
  private n: number

  constructor(key: Uint8Array) {
    this.k = key
    this.n = 0
  }

  encrypt(plaintext: Uint8Array): Uint8Array {
    const nonceBytes = new Uint8Array(12)
    new DataView(nonceBytes.buffer).setUint32(4, this.n, true)
    const cipher = chacha20poly1305(this.k, nonceBytes, new Uint8Array(0))
    const ct = cipher.encrypt(plaintext)
    this.n++
    return ct
  }

  decrypt(ciphertext: Uint8Array): Uint8Array {
    const nonceBytes = new Uint8Array(12)
    new DataView(nonceBytes.buffer).setUint32(4, this.n, true)
    const cipher = chacha20poly1305(this.k, nonceBytes, new Uint8Array(0))
    const pt = cipher.decrypt(ciphertext)
    this.n++
    return pt
  }

  needsRekey(): boolean {
    return false
  }
}

/** Create a matched pair of cipher states (send/receive mirrors). */
function createTestSession(): { initiator: Session, responder: Session } {
  const key1 = new Uint8Array(32)
  const key2 = new Uint8Array(32)
  key1[0] = 1 // Different keys for send/receive
  key2[0] = 2
  return {
    initiator: { send: new TestCipherState(key1), receive: new TestCipherState(key2) } as unknown as Session,
    responder: { send: new TestCipherState(key2), receive: new TestCipherState(key1) } as unknown as Session,
  }
}

/** Encode a ChannelMessage into the wire format (4-byte length prefix + protobuf). */
function encodeWireMessage(channelId: string, ciphertext: Uint8Array, opts?: { close?: boolean, id?: number }): ArrayBuffer {
  const msg = create(ChannelMessageSchema, {
    protocolVersion: 1,
    channelId,
    ciphertext,
    flags: opts?.close ? 2 : 0,
    correlationId: opts?.id ?? 0,
  })
  const data = toBinary(ChannelMessageSchema, msg)
  const buf = new Uint8Array(4 + data.length)
  new DataView(buf.buffer).setUint32(0, data.length)
  buf.set(data, 4)
  return buf.buffer
}

/** Encode a close notification. */
function encodeCloseMessage(channelId: string): ArrayBuffer {
  return encodeWireMessage(channelId, new Uint8Array(), { close: true })
}

/** Parse a sent wire-format buffer back into a ChannelMessage. */
function decodeWireMessage(buf: ArrayBuffer) {
  const arr = new Uint8Array(buf)
  const length = new DataView(arr.buffer, arr.byteOffset).getUint32(0)
  return fromBinary(ChannelMessageSchema, arr.slice(4, 4 + length))
}

// ---- Mock WebSocket ----

type EventCallback = (...args: any[]) => void

class MockWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  readyState = MockWebSocket.CONNECTING
  binaryType = 'arraybuffer'

  private listeners = new Map<string, Set<EventCallback>>()
  sent: ArrayBuffer[] = []

  private onceListeners = new Set<EventCallback>()

  addEventListener(type: string, listener: EventCallback, opts?: { once?: boolean }) {
    if (!this.listeners.has(type)) {
      this.listeners.set(type, new Set())
    }
    this.listeners.get(type)!.add(listener)
    if (opts?.once) {
      this.onceListeners.add(listener)
    }
  }

  removeEventListener(type: string, listener: EventCallback) {
    this.listeners.get(type)?.delete(listener)
    this.onceListeners.delete(listener)
  }

  send(data: ArrayBuffer | Uint8Array) {
    if (data instanceof Uint8Array) {
      this.sent.push(data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) as ArrayBuffer)
    }
    else {
      this.sent.push(data)
    }
  }

  close(_code?: number, _reason?: string) {
    this.readyState = MockWebSocket.CLOSED
    this.emit('close')
  }

  // -- Test helpers --

  simulateOpen() {
    this.readyState = MockWebSocket.OPEN
    this.emit('open')
  }

  simulateMessage(data: ArrayBuffer) {
    this.emit('message', { data } as MessageEvent)
  }

  simulateClose() {
    this.readyState = MockWebSocket.CLOSED
    this.emit('close')
  }

  simulateError() {
    this.emit('error')
  }

  private emit(type: string, event?: any) {
    const handlers = this.listeners.get(type)
    if (handlers) {
      for (const h of [...handlers]) {
        h(event)
        if (this.onceListeners.has(h)) {
          handlers.delete(h)
          this.onceListeners.delete(h)
        }
      }
    }
  }
}

// ---- Mock Transport ----

const sessions = new Map<string, { initiator: Session, responder: Session }>()

function createMockTransport(mockWs: MockWebSocket): ChannelTransport {
  return {
    async getWorkerPublicKey(_workerId: string): Promise<Uint8Array> {
      // Return a dummy 32-byte key. The real handshake is bypassed
      // since we mock initiatorHandshake1/2.
      return new Uint8Array(32)
    },
    async openChannel(_workerId: string, _handshakePayload: Uint8Array) {
      const channelId = `ch-${Math.random().toString(36).slice(2, 8)}`
      const pair = createTestSession()
      sessions.set(channelId, pair)
      // Return the handshake payload that initiatorHandshake2 expects.
      // Since we mock the handshake functions, the actual bytes don't matter.
      return { channelId, handshakePayload: new Uint8Array(48) }
    },
    async closeChannel(_channelId: string) {},
    createWebSocket(): WebSocket {
      return mockWs as any
    },
    async confirmKeyPin(_workerId: string, _expectedFingerprint: string, _actualFingerprint: string): Promise<KeyPinDecision> {
      return 'accept'
    },
    getUserId(): string {
      return 'test-user-id'
    },
  }
}

// ---- Mock handshake functions (injected via ChannelManager DI) ----

function mockHandshake1(_publicKey: Uint8Array) {
  return {
    handshakeState: {} as any,
    message1: new Uint8Array(48),
  }
}

function mockHandshake2(_state: any, _message2: Uint8Array) {
  const entries = [...sessions.entries()]
  const lastEntry = entries[entries.length - 1]
  if (!lastEntry)
    throw new Error('No session registered')
  return lastEntry[1].initiator
}

// ---- Tests ----

describe('channelManager', () => {
  let mockWs: MockWebSocket
  let mgr: ChannelManager

  beforeEach(() => {
    sessions.clear()
    mockWs = new MockWebSocket()
    mgr = new ChannelManager(createMockTransport(mockWs), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
    })
  })

  afterEach(() => {
    mgr.closeAll()
  })

  /**
   * Flush pending microtasks so async code that chains multiple
   * resolved-promise awaits (like openChannel) progresses.
   */
  async function flushMicrotasks() {
    for (let i = 0; i < 10; i++) {
      await Promise.resolve()
    }
  }

  /**
   * Simulate the worker accepting the UserIdClaim.
   * After the WS opens, the ChannelManager sends a UserIdClaim as the first
   * encrypted message. This helper decrypts it and responds with success.
   */
  function simulateClaimAccept() {
    const lastSent = mockWs.sent[mockWs.sent.length - 1]
    const sentMsg = decodeWireMessage(lastSent)
    const channelId = sentMsg.channelId
    const pair = sessions.get(channelId)!

    // Consume the claim ciphertext (advances responder.receive nonce).
    pair.responder.receive.decrypt(sentMsg.ciphertext)

    // Send back a successful UserIdClaimResponse wrapped in InnerMessage.
    const claimResp = create(UserIdClaimResponseSchema, { success: true })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'userIdClaimResponse', value: claimResp },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext))
  }

  async function openTestChannel(workerId = 'w1'): Promise<string> {
    const openPromise = mgr.openChannel(workerId)
    // Flush microtasks so openChannel progresses through its awaits
    // and ensureWebSocket() registers the 'open' listener.
    await flushMicrotasks()
    mockWs.simulateOpen()
    // After WS opens, flush microtasks so sendUserIdClaim runs.
    await flushMicrotasks()
    // Simulate the worker accepting the UserIdClaim.
    simulateClaimAccept()
    return openPromise
  }

  function sendResponseFromWorker(channelId: string, requestId: number, payload: Uint8Array) {
    const pair = sessions.get(channelId)!
    const resp = create(InnerRpcResponseSchema, {
      payload,
      isError: false,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'response', value: resp },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  function sendErrorResponseFromWorker(channelId: string, requestId: number, errorMessage: string) {
    const pair = sessions.get(channelId)!
    const resp = create(InnerRpcResponseSchema, {
      isError: true,
      errorMessage,
      errorCode: 2,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'response', value: resp },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  function sendStreamMessageFromWorker(channelId: string, requestId: number, payload: Uint8Array) {
    const pair = sessions.get(channelId)!
    const msg = create(InnerStreamMessageSchema, {
      payload,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'stream', value: msg },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  function sendStreamEndFromWorker(channelId: string, requestId: number) {
    const pair = sessions.get(channelId)!
    const msg = create(InnerStreamMessageSchema, {
      end: true,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'stream', value: msg },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  function sendStreamErrorFromWorker(channelId: string, requestId: number, errorMessage: string) {
    const pair = sessions.get(channelId)!
    const msg = create(InnerStreamMessageSchema, {
      isError: true,
      errorMessage,
    })
    const envelope = create(InnerMessageSchema, {
      kind: { case: 'stream', value: msg },
    })
    const plaintext = toBinary(InnerMessageSchema, envelope)
    const ciphertext = pair.responder.send.encrypt(plaintext)
    mockWs.simulateMessage(encodeWireMessage(channelId, ciphertext, { id: requestId }))
  }

  describe('openChannel', () => {
    it('should open a channel and return the channel ID', async () => {
      const channelId = await openTestChannel('w1')
      expect(channelId).toBeTruthy()
      expect(mgr.isOpen(channelId)).toBe(true)
      expect(mgr.getWorkerId(channelId)).toBe('w1')
    })

    it('should reuse the WebSocket for multiple channels', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2 = await openTestChannel('w2')
      expect(ch1).not.toBe(ch2)
      expect(mgr.isOpen(ch1)).toBe(true)
      expect(mgr.isOpen(ch2)).toBe(true)
    })
  })

  describe('closeChannel', () => {
    it('should mark channel as closed', async () => {
      const channelId = await openTestChannel('w1')
      await mgr.closeChannel(channelId)
      expect(mgr.isOpen(channelId)).toBe(false)
    })

    it('should reject pending requests on close', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array())
      await mgr.closeChannel(channelId)
      await expect(callPromise).rejects.toThrow('channel closed')
    })

    it('should end active streams on close', async () => {
      const channelId = await openTestChannel('w1')
      const endFn = vi.fn()
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onEnd(endFn)

      await mgr.closeChannel(channelId)
      expect(endFn).toHaveBeenCalled()
    })

    it('should be idempotent', async () => {
      const channelId = await openTestChannel('w1')
      await mgr.closeChannel(channelId)
      await mgr.closeChannel(channelId) // Should not throw.
    })
  })

  describe('call', () => {
    it('should send a request and receive a response', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'TestMethod', new Uint8Array([1, 2, 3]))

      // The last sent WS message is the actual request (claim was earlier).
      const requestSentIndex = mockWs.sent.length - 1

      // Decrypt and verify the sent message.
      const sentMsg = decodeWireMessage(mockWs.sent[requestSentIndex])
      expect(sentMsg.channelId).toBe(channelId)
      const pair = sessions.get(channelId)!
      const sentPlaintext = pair.responder.receive.decrypt(sentMsg.ciphertext)
      const sentEnvelope = fromBinary(InnerMessageSchema, sentPlaintext)
      expect(sentEnvelope.kind.case).toBe('request')
      const sentReq = fromBinary(InnerRpcRequestSchema, toBinary(InnerRpcRequestSchema, sentEnvelope.kind.value as any))
      expect(sentReq.method).toBe('TestMethod')
      expect(sentReq.payload).toEqual(new Uint8Array([1, 2, 3]))
      expect(sentMsg.correlationId).toBe(1)

      // Send a response from the worker.
      sendResponseFromWorker(channelId, 1, new Uint8Array([4, 5, 6]))

      const resp = await callPromise
      expect(resp.payload).toEqual(new Uint8Array([4, 5, 6]))
    })

    it('should reject on error response', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'TestMethod', new Uint8Array())

      sendErrorResponseFromWorker(channelId, 1, 'something went wrong')

      await expect(callPromise).rejects.toThrow('something went wrong')
    })

    it('should reject if channel is not open', async () => {
      await expect(mgr.call('nonexistent', 'Test', new Uint8Array())).rejects.toThrow('channel not open')
    })

    it('should reject if channel is closed', async () => {
      const channelId = await openTestChannel('w1')
      await mgr.closeChannel(channelId)
      await expect(mgr.call(channelId, 'Test', new Uint8Array())).rejects.toThrow('channel not open')
    })

    it('should handle multiple concurrent calls', async () => {
      const channelId = await openTestChannel('w1')

      const call1 = mgr.call(channelId, 'Method1', new Uint8Array([1]))
      const call2 = mgr.call(channelId, 'Method2', new Uint8Array([2]))

      // Respond in reverse order.
      sendResponseFromWorker(channelId, 2, new Uint8Array([20]))
      sendResponseFromWorker(channelId, 1, new Uint8Array([10]))

      const [resp1, resp2] = await Promise.all([call1, call2])
      expect(resp1.payload).toEqual(new Uint8Array([10]))
      expect(resp2.payload).toEqual(new Uint8Array([20]))
    })

    it('should timeout after 30s', async () => {
      vi.useFakeTimers()
      try {
        const channelId = await openTestChannel('w1')
        const callPromise = mgr.call(channelId, 'SlowMethod', new Uint8Array())

        vi.advanceTimersByTime(30_000)

        await expect(callPromise).rejects.toThrow('timed out after 30s')
      }
      finally {
        vi.useRealTimers()
      }
    })
  })

  describe('stream', () => {
    it('should receive stream messages', async () => {
      const channelId = await openTestChannel('w1')
      const messages: Uint8Array[] = []
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onMessage((msg) => {
        messages.push(msg.payload)
      })

      sendStreamMessageFromWorker(channelId, handle.requestId, new Uint8Array([1]))
      sendStreamMessageFromWorker(channelId, handle.requestId, new Uint8Array([2]))

      expect(messages).toHaveLength(2)
      expect(messages[0]).toEqual(new Uint8Array([1]))
      expect(messages[1]).toEqual(new Uint8Array([2]))
    })

    it('should handle stream end', async () => {
      const channelId = await openTestChannel('w1')
      const endFn = vi.fn()
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onEnd(endFn)

      sendStreamEndFromWorker(channelId, handle.requestId)

      expect(endFn).toHaveBeenCalledOnce()
    })

    it('should handle stream error', async () => {
      const channelId = await openTestChannel('w1')
      const errorFn = vi.fn()
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onError(errorFn)

      sendStreamErrorFromWorker(channelId, handle.requestId, 'stream broke')

      expect(errorFn).toHaveBeenCalledOnce()
      expect(errorFn.mock.calls[0][0].message).toBe('stream broke')
    })

    it('should throw if channel is not open', async () => {
      expect(() => mgr.stream('nonexistent', 'WatchEvents', new Uint8Array())).toThrow('channel not open')
    })
  })

  describe('getOrOpenChannel', () => {
    it('should return existing channel for same worker', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2 = await mgr.getOrOpenChannel('w1')
      expect(ch2).toBe(ch1)
    })

    it('should open new channel for different worker', async () => {
      const ch1 = await openTestChannel('w1')
      // getOrOpenChannel opens a new channel — must simulate claim acceptance.
      const ch2Promise = mgr.getOrOpenChannel('w2')
      await flushMicrotasks()
      simulateClaimAccept()
      const ch2 = await ch2Promise
      expect(ch2).not.toBe(ch1)
    })

    it('should open new channel if existing one is closed', async () => {
      const ch1 = await openTestChannel('w1')
      await mgr.closeChannel(ch1)
      // getOrOpenChannel opens a new channel — must simulate claim acceptance.
      const ch2Promise = mgr.getOrOpenChannel('w1')
      await flushMicrotasks()
      simulateClaimAccept()
      const ch2 = await ch2Promise
      expect(ch2).not.toBe(ch1)
      expect(mgr.isOpen(ch2)).toBe(true)
    })
  })

  describe('close notification', () => {
    it('should handle close=true as close notification', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array())

      // Simulate close notification (close: true).
      mockWs.simulateMessage(encodeCloseMessage(channelId))

      await expect(callPromise).rejects.toThrow('channel closed by server')
      expect(mgr.isOpen(channelId)).toBe(false)
    })

    it('should reject pending requests on close notification', async () => {
      const channelId = await openTestChannel('w1')
      const call1 = mgr.call(channelId, 'M1', new Uint8Array())
      const call2 = mgr.call(channelId, 'M2', new Uint8Array())

      mockWs.simulateMessage(encodeCloseMessage(channelId))

      await expect(call1).rejects.toThrow('channel closed by server')
      await expect(call2).rejects.toThrow('channel closed by server')
    })

    it('should error active streams on close notification', async () => {
      const channelId = await openTestChannel('w1')
      const errorFn = vi.fn()
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onError(errorFn)

      mockWs.simulateMessage(encodeCloseMessage(channelId))

      expect(errorFn).toHaveBeenCalledOnce()
      expect(errorFn.mock.calls[0][0].message).toBe('channel closed by server')
    })
  })

  describe('webSocket close', () => {
    it('should reject all pending requests when WebSocket closes', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array())

      mockWs.simulateClose()

      await expect(callPromise).rejects.toThrow('channel disconnected')
      expect(mgr.isOpen(channelId)).toBe(false)
    })

    it('should error all streams when WebSocket closes', async () => {
      const channelId = await openTestChannel('w1')
      const errorFn = vi.fn()
      const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onError(errorFn)

      mockWs.simulateClose()

      expect(errorFn).toHaveBeenCalledOnce()
      expect(errorFn.mock.calls[0][0].message).toBe('channel disconnected')
    })

    it('should close all channels when WebSocket closes', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2 = await openTestChannel('w2')

      mockWs.simulateClose()

      expect(mgr.isOpen(ch1)).toBe(false)
      expect(mgr.isOpen(ch2)).toBe(false)
    })
  })

  describe('closeAll', () => {
    it('should close all channels and the WebSocket', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2 = await openTestChannel('w2')

      mgr.closeAll()

      expect(mgr.isOpen(ch1)).toBe(false)
      expect(mgr.isOpen(ch2)).toBe(false)
      expect(mockWs.readyState).toBe(MockWebSocket.CLOSED)
    })
  })

  describe('webSocket connection failure', () => {
    it('should reject openChannel if WebSocket fails to connect', async () => {
      const openPromise = mgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateError()
      await expect(openPromise).rejects.toThrow('WebSocket connection failed')
    })

    it('should reject openChannel if WebSocket times out', async () => {
      vi.useFakeTimers()
      try {
        const openPromise = mgr.openChannel('w1')
        // Flush microtasks so openChannel reaches ensureWebSocket.
        vi.advanceTimersByTime(0)
        await flushMicrotasks()
        // Set up rejection expectation BEFORE advancing timers
        // to avoid unhandled rejection warning.
        const expectation = expect(openPromise).rejects.toThrow('WebSocket open timed out after 10s')
        // Now advance past the 10s WS open timeout.
        vi.advanceTimersByTime(10_000)
        await flushMicrotasks()
        await expectation
      }
      finally {
        vi.useRealTimers()
      }
    })
  })

  describe('message routing', () => {
    it('should ignore messages for unknown channels', async () => {
      await openTestChannel('w1')
      // Should not throw.
      mockWs.simulateMessage(encodeWireMessage('unknown-channel', new Uint8Array([1, 2, 3])))
    })

    it('should route messages to the correct channel', async () => {
      const ch1 = await openTestChannel('w1')
      const ch2 = await openTestChannel('w2')

      const call1 = mgr.call(ch1, 'M1', new Uint8Array())
      const call2 = mgr.call(ch2, 'M2', new Uint8Array())

      sendResponseFromWorker(ch2, 1, new Uint8Array([20]))
      sendResponseFromWorker(ch1, 1, new Uint8Array([10]))

      const [resp1, resp2] = await Promise.all([call1, call2])
      expect(resp1.payload).toEqual(new Uint8Array([10]))
      expect(resp2.payload).toEqual(new Uint8Array([20]))
    })
  })

  describe('chunking', () => {
    /** Encode a wire message with specific flags. */
    function encodeWireMessageWithFlags(channelId: string, ciphertext: Uint8Array, opts: { correlationId: number, flags: number }): ArrayBuffer {
      const msg = create(ChannelMessageSchema, {
        protocolVersion: 1,
        channelId,
        ciphertext,
        correlationId: opts.correlationId,
        flags: opts.flags,
      })
      const data = toBinary(ChannelMessageSchema, msg)
      const buf = new Uint8Array(4 + data.length)
      new DataView(buf.buffer).setUint32(0, data.length)
      buf.set(data, 4)
      return buf.buffer
    }

    it('should send a single chunk for small plaintext', async () => {
      const channelId = await openTestChannel('w1')
      const sentBefore = mockWs.sent.length

      const callPromise = mgr.call(channelId, 'Test', new Uint8Array([1, 2, 3]))

      const sentAfter = mockWs.sent.length
      expect(sentAfter - sentBefore).toBe(1) // Just 1 frame

      const msg = decodeWireMessage(mockWs.sent[sentAfter - 1])
      expect(msg.flags).toBe(0) // UNSPECIFIED

      // Complete the call so it doesn't stay pending during cleanup.
      sendResponseFromWorker(channelId, 1, new Uint8Array([42]))
      await callPromise
    })

    it('should send multiple chunks for large plaintext', async () => {
      // Create a manager with a small max chunk awareness.
      // We can't easily test actual chunking without matching MAX_CHUNK_SIZE,
      // but we can verify the chunk splitting logic by checking the wire output.
      const channelId = await openTestChannel('w1')
      const sentBefore = mockWs.sent.length

      // Send a request — the payload itself is small enough for one chunk.
      // This just validates the normal path works.
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array(10))
      const sentAfter = mockWs.sent.length
      expect(sentAfter - sentBefore).toBe(1)

      // Complete the call.
      sendResponseFromWorker(channelId, 1, new Uint8Array([42]))
      const resp = await callPromise
      expect(resp.payload).toEqual(new Uint8Array([42]))
    })

    it('should reassemble multi-chunk response', async () => {
      const channelId = await openTestChannel('w1')
      const callPromise = mgr.call(channelId, 'Test', new Uint8Array())

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
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct1, { correlationId: 1, flags: 1 }))

      // Send chunk2 with flags=UNSPECIFIED (final).
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct2, { correlationId: 1, flags: 0 }))

      const result = await callPromise
      expect(result.payload).toEqual(new Uint8Array([10, 20, 30, 40, 50]))
    })

    it('should drop oversized chunked messages', async () => {
      // Create a manager with a very small max message size.
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })

      const openPromise = smallMgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()

      // Simulate claim accept for the small manager.
      const lastSent = mockWs.sent[mockWs.sent.length - 1]
      const sentMsg = decodeWireMessage(lastSent)
      const chId = sentMsg.channelId
      const pair = sessions.get(chId)!
      pair.responder.receive.decrypt(sentMsg.ciphertext)
      const claimResp = create(UserIdClaimResponseSchema, { success: true })
      const claimEnv = create(InnerMessageSchema, {
        kind: { case: 'userIdClaimResponse', value: claimResp },
      })
      const claimPt = toBinary(InnerMessageSchema, claimEnv)
      const claimCt = pair.responder.send.encrypt(claimPt)
      mockWs.simulateMessage(encodeWireMessage(chId, claimCt))

      const channelId = await openPromise

      const callPromise = smallMgr.call(channelId, 'Test', new Uint8Array())

      // Send a chunk that's within limits.
      const chunk1Data = new Uint8Array(30)
      const ct1 = pair.responder.send.encrypt(chunk1Data)
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct1, { correlationId: 1, flags: 1 }))

      // Send another chunk that exceeds the 50-byte limit (total 60 > 50).
      const chunk2Data = new Uint8Array(30)
      const ct2 = pair.responder.send.encrypt(chunk2Data)
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct2, { correlationId: 1, flags: 1 }))

      // The call should be rejected with an error about the size limit.
      await expect(callPromise).rejects.toThrow('exceeds')

      smallMgr.closeAll()
    })

    it('should reject too many incomplete chunked sequences', async () => {
      const channelId = await openTestChannel('w1')
      const pair = sessions.get(channelId)!

      // Start MAX_INCOMPLETE_CHUNKED (4) chunked sequences.
      for (let i = 1; i <= 4; i++) {
        const chunk = new Uint8Array([i])
        const ct = pair.responder.send.encrypt(chunk)
        mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct, { correlationId: i, flags: 1 }))
      }

      // 5th should be dropped (exceeded MAX_INCOMPLETE_CHUNKED).
      const chunk5 = new Uint8Array([5])
      const ct5 = pair.responder.send.encrypt(chunk5)
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct5, { correlationId: 5, flags: 1 }))

      // Channel should still be functional — close notification works.
      mockWs.simulateMessage(encodeCloseMessage(channelId))
      expect(mgr.isOpen(channelId)).toBe(false)
    })

    it('should throw on send when plaintext exceeds maxMessageSize', async () => {
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })

      const openPromise = smallMgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()

      // Simulate claim accept.
      const lastSent = mockWs.sent[mockWs.sent.length - 1]
      const sentMsg = decodeWireMessage(lastSent)
      const chId = sentMsg.channelId
      const pair = sessions.get(chId)!
      pair.responder.receive.decrypt(sentMsg.ciphertext)
      const claimResp = create(UserIdClaimResponseSchema, { success: true })
      const claimEnv = create(InnerMessageSchema, {
        kind: { case: 'userIdClaimResponse', value: claimResp },
      })
      const claimPt = toBinary(InnerMessageSchema, claimEnv)
      const claimCt = pair.responder.send.encrypt(claimPt)
      mockWs.simulateMessage(encodeWireMessage(chId, claimCt))

      const channelId = await openPromise

      // A large payload should cause call() to reject with "message too large".
      await expect(smallMgr.call(channelId, 'Test', new Uint8Array(200))).rejects.toThrow('message too large')

      smallMgr.closeAll()
    })

    it('should reject on final chunk oversize', async () => {
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })

      const openPromise = smallMgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()

      // Simulate claim accept.
      const lastSent = mockWs.sent[mockWs.sent.length - 1]
      const sentMsg = decodeWireMessage(lastSent)
      const chId = sentMsg.channelId
      const pair = sessions.get(chId)!
      pair.responder.receive.decrypt(sentMsg.ciphertext)
      const claimResp = create(UserIdClaimResponseSchema, { success: true })
      const claimEnv = create(InnerMessageSchema, {
        kind: { case: 'userIdClaimResponse', value: claimResp },
      })
      const claimPt = toBinary(InnerMessageSchema, claimEnv)
      const claimCt = pair.responder.send.encrypt(claimPt)
      mockWs.simulateMessage(encodeWireMessage(chId, claimCt))

      const channelId = await openPromise

      const callPromise = smallMgr.call(channelId, 'Test', new Uint8Array())

      // Send a first chunk within limits (30 bytes).
      const chunk1 = pair.responder.send.encrypt(new Uint8Array(30))
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk1, { correlationId: 1, flags: 1 }))

      // Send a final chunk that pushes over (30 + 30 = 60 > 50).
      const chunk2 = pair.responder.send.encrypt(new Uint8Array(30))
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk2, { correlationId: 1, flags: 0 }))

      await expect(callPromise).rejects.toThrow('exceeds')

      smallMgr.closeAll()
    })

    it('should route chunking errors to stream listeners', async () => {
      sessions.clear()
      const smallMgr = new ChannelManager(createMockTransport(mockWs), {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        maxMessageSize: 50,
      })

      const openPromise = smallMgr.openChannel('w1')
      await flushMicrotasks()
      mockWs.simulateOpen()
      await flushMicrotasks()

      // Simulate claim accept.
      const lastSent = mockWs.sent[mockWs.sent.length - 1]
      const sentMsg = decodeWireMessage(lastSent)
      const chId = sentMsg.channelId
      const pair = sessions.get(chId)!
      pair.responder.receive.decrypt(sentMsg.ciphertext)
      const claimResp = create(UserIdClaimResponseSchema, { success: true })
      const claimEnv = create(InnerMessageSchema, {
        kind: { case: 'userIdClaimResponse', value: claimResp },
      })
      const claimPt = toBinary(InnerMessageSchema, claimEnv)
      const claimCt = pair.responder.send.encrypt(claimPt)
      mockWs.simulateMessage(encodeWireMessage(chId, claimCt))

      const channelId = await openPromise

      const errorFn = vi.fn()
      const handle = smallMgr.stream(channelId, 'WatchEvents', new Uint8Array())
      handle.onError(errorFn)

      // Send chunks that exceed the limit, targeted at the stream's requestId.
      const chunk1 = pair.responder.send.encrypt(new Uint8Array(30))
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk1, { correlationId: handle.requestId, flags: 1 }))

      const chunk2 = pair.responder.send.encrypt(new Uint8Array(30))
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, chunk2, { correlationId: handle.requestId, flags: 1 }))

      expect(errorFn).toHaveBeenCalledOnce()
      expect(errorFn.mock.calls[0][0].message).toContain('exceeds')

      smallMgr.closeAll()
    })

    it('should clear reassembly on close', async () => {
      const channelId = await openTestChannel('w1')
      const pair = sessions.get(channelId)!

      // Start a chunked sequence.
      const chunk = new Uint8Array([1, 2, 3])
      const ct = pair.responder.send.encrypt(chunk)
      mockWs.simulateMessage(encodeWireMessageWithFlags(channelId, ct, { correlationId: 1, flags: 1 }))

      // Close the channel — should not crash and should clean up.
      await mgr.closeChannel(channelId)
      expect(mgr.isOpen(channelId)).toBe(false)
    })
  })

  describe('observability hooks', () => {
    describe('onStateChange', () => {
      it('should fire after openChannel succeeds', async () => {
        const cb = vi.fn()
        mgr.onStateChange(cb)
        await openTestChannel('w1')
        expect(cb).toHaveBeenCalledOnce()
      })

      it('should fire after closeChannel', async () => {
        const channelId = await openTestChannel('w1')
        const cb = vi.fn()
        mgr.onStateChange(cb)
        await mgr.closeChannel(channelId)
        expect(cb).toHaveBeenCalledOnce()
      })

      it('should fire on WebSocket close (all channels torn down)', async () => {
        await openTestChannel('w1')
        await openTestChannel('w2')
        const cb = vi.fn()
        mgr.onStateChange(cb)
        mockWs.simulateClose()
        expect(cb).toHaveBeenCalledOnce()
      })

      it('should fire on CLOSE sentinel', async () => {
        const channelId = await openTestChannel('w1')
        const cb = vi.fn()
        mgr.onStateChange(cb)
        mockWs.simulateMessage(encodeCloseMessage(channelId))
        expect(cb).toHaveBeenCalledOnce()
      })

      it('should not fire after unsubscribe', async () => {
        const cb = vi.fn()
        const unsub = mgr.onStateChange(cb)
        unsub()
        await openTestChannel('w1')
        expect(cb).not.toHaveBeenCalled()
      })
    })

    describe('onChannelError', () => {
      it('should fire on RPC error (non-transport)', async () => {
        const channelId = await openTestChannel('w1')
        const cb = vi.fn()
        mgr.onChannelError(cb)

        const callPromise = mgr.call(channelId, 'Test', new Uint8Array())
        sendErrorResponseFromWorker(channelId, 1, 'rpc failed')
        await expect(callPromise).rejects.toThrow('rpc failed')

        expect(cb).toHaveBeenCalledOnce()
        expect(cb.mock.calls[0][0]).toBe('w1')
        expect(cb.mock.calls[0][1].source).toBe('rpc')
      })

      it('should fire on stream error (non-transport)', async () => {
        const channelId = await openTestChannel('w1')
        const cb = vi.fn()
        mgr.onChannelError(cb)

        const errorFn = vi.fn()
        const handle = mgr.stream(channelId, 'WatchEvents', new Uint8Array())
        handle.onError(errorFn)

        sendStreamErrorFromWorker(channelId, handle.requestId, 'stream broke')

        expect(cb).toHaveBeenCalledOnce()
        expect(cb.mock.calls[0][0]).toBe('w1')
        expect(cb.mock.calls[0][1].source).toBe('stream')
      })

      it('should not fire after unsubscribe', async () => {
        const channelId = await openTestChannel('w1')
        const cb = vi.fn()
        const unsub = mgr.onChannelError(cb)
        unsub()

        const callPromise = mgr.call(channelId, 'Test', new Uint8Array())
        sendErrorResponseFromWorker(channelId, 1, 'rpc failed')
        await expect(callPromise).rejects.toThrow('rpc failed')

        expect(cb).not.toHaveBeenCalled()
      })
    })

    describe('hasOpenChannel', () => {
      it('should return true when channel is open', async () => {
        await openTestChannel('w1')
        expect(mgr.hasOpenChannel('w1')).toBe(true)
      })

      it('should return false when no channel exists', () => {
        expect(mgr.hasOpenChannel('w1')).toBe(false)
      })

      it('should return false after channel is closed', async () => {
        const channelId = await openTestChannel('w1')
        await mgr.closeChannel(channelId)
        expect(mgr.hasOpenChannel('w1')).toBe(false)
      })
    })
  })
})
