import type { ChannelTransport } from './channel'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChannelError, ChannelManager } from './channel'
import {
  ChannelManagerTestHarness,
  createMockTransport,
  decodeWireMessage,
  encodeCloseMessage,
  encodeWireMessage,
  mockHandshake1,
  mockHandshake2,
  MockWebSocket,
} from './channel.test-support'

describe('channelManager closeChannel', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should mark channel as closed', async () => {
    const channelId = await h.openTestChannel('w1')
    await h.mgr.closeChannel(channelId)
    expect(h.mgr.isOpen(channelId)).toBe(false)
  })

  it('should reject pending requests on close', async () => {
    const channelId = await h.openTestChannel('w1')
    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array())
    await h.mgr.closeChannel(channelId)
    await expect(callPromise).rejects.toThrow('channel closed')
  })

  it('should end active streams on close', async () => {
    const channelId = await h.openTestChannel('w1')
    const endFn = vi.fn()
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onEnd(endFn)

    await h.mgr.closeChannel(channelId)
    expect(endFn).toHaveBeenCalled()
  })

  it('should be idempotent', async () => {
    const channelId = await h.openTestChannel('w1')
    await h.mgr.closeChannel(channelId)
    await h.mgr.closeChannel(channelId) // Should not throw.
  })

  // A teardown that races the open must REJECT the pending openChannel, not hang.
  //
  // These three cases were covered while the open awaited a UserIdClaim; the open
  // now awaits a Ping instead, so the same three teardowns must settle the Ping's
  // pendingRequests entry rather than the claim's. The hang they guard against is
  // silent and total: getOrOpenChannel keeps the unresolved promise in its dedup
  // map, so every later caller for that worker awaits it forever with no error and
  // no timeout.
  it('rejects the pending openChannel when closeChannel races the open', async () => {
    const openPromise = h.mgr.openChannel('w1')
    await h.flushMicrotasks()
    h.mockWs.simulateOpen()
    await h.flushMicrotasks()
    // The Ping is now in flight; the worker has not answered it.
    const channelId = decodeWireMessage(h.mockWs.sent.at(-1)!).channelId
    await h.mgr.closeChannel(channelId)
    await expect(openPromise).rejects.toThrow(ChannelError)
    await expect(openPromise).rejects.toThrow('channel closed')
  })

  it('rejects the pending openChannel when the server sends CLOSE during the open', async () => {
    const openPromise = h.mgr.openChannel('w1')
    await h.flushMicrotasks()
    h.mockWs.simulateOpen()
    await h.flushMicrotasks()
    const channelId = decodeWireMessage(h.mockWs.sent.at(-1)!).channelId
    h.mockWs.simulateMessage(encodeCloseMessage(channelId))
    await expect(openPromise).rejects.toThrow(ChannelError)
    await expect(openPromise).rejects.toThrow('channel closed by server')
  })

  it('rejects the pending openChannel when the WebSocket closes during the open', async () => {
    const openPromise = h.mgr.openChannel('w1')
    await h.flushMicrotasks()
    h.mockWs.simulateOpen()
    await h.flushMicrotasks()
    h.mockWs.simulateClose()
    await expect(openPromise).rejects.toThrow(ChannelError)
    await expect(openPromise).rejects.toThrow('channel disconnected')
  })
})

describe('channelManager close notification', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should handle close=true as close notification', async () => {
    const channelId = await h.openTestChannel('w1')
    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array())

    // Simulate close notification (close: true).
    h.mockWs.simulateMessage(encodeCloseMessage(channelId))

    await expect(callPromise).rejects.toThrow('channel closed by server')
    expect(h.mgr.isOpen(channelId)).toBe(false)
  })

  it('should reject pending requests on close notification', async () => {
    const channelId = await h.openTestChannel('w1')
    const call1 = h.mgr.call(channelId, 'M1', new Uint8Array())
    const call2 = h.mgr.call(channelId, 'M2', new Uint8Array())

    h.mockWs.simulateMessage(encodeCloseMessage(channelId))

    await expect(call1).rejects.toThrow('channel closed by server')
    await expect(call2).rejects.toThrow('channel closed by server')
  })

  it('should error active streams on close notification', async () => {
    const channelId = await h.openTestChannel('w1')
    const errorFn = vi.fn()
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onError(errorFn)

    h.mockWs.simulateMessage(encodeCloseMessage(channelId))

    expect(errorFn).toHaveBeenCalledOnce()
    expect(errorFn.mock.calls[0][0].message).toBe('channel closed by server')
  })
})

describe('channelManager decrypt failure', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should close the channel on decrypt failure', async () => {
    const channelId = await h.openTestChannel('w1')
    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array())

    // Send a message with corrupted ciphertext (not encrypted with the correct key/nonce).
    const corruptCiphertext = new Uint8Array(32)
    corruptCiphertext.fill(0xFF)
    h.mockWs.simulateMessage(encodeWireMessage(channelId, corruptCiphertext, { id: 1 }))

    // The channel should be closed and the pending request rejected.
    await expect(callPromise).rejects.toThrow('channel closed')
    expect(h.mgr.isOpen(channelId)).toBe(false)
  })

  it('should close only the affected channel on decrypt failure', async () => {
    const ch1 = await h.openTestChannel('w1')
    const ch2 = await h.openTestChannel('w2')

    // Corrupt ciphertext on ch1.
    const corruptCiphertext = new Uint8Array(32)
    corruptCiphertext.fill(0xFF)
    h.mockWs.simulateMessage(encodeWireMessage(ch1, corruptCiphertext, { id: 1 }))

    expect(h.mgr.isOpen(ch1)).toBe(false)
    expect(h.mgr.isOpen(ch2)).toBe(true)
  })

  it('should error active streams on decrypt failure', async () => {
    const channelId = await h.openTestChannel('w1')
    const endFn = vi.fn()
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onEnd(endFn)

    const corruptCiphertext = new Uint8Array(32)
    corruptCiphertext.fill(0xFF)
    h.mockWs.simulateMessage(encodeWireMessage(channelId, corruptCiphertext, { id: 1 }))

    expect(endFn).toHaveBeenCalledOnce()
    expect(h.mgr.isOpen(channelId)).toBe(false)
  })
})

describe('channelManager webSocket close', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should reject all pending requests when WebSocket closes', async () => {
    const channelId = await h.openTestChannel('w1')
    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array())

    h.mockWs.simulateClose()

    await expect(callPromise).rejects.toThrow('channel disconnected')
    expect(h.mgr.isOpen(channelId)).toBe(false)
  })

  it('should error all streams when WebSocket closes', async () => {
    const channelId = await h.openTestChannel('w1')
    const errorFn = vi.fn()
    const handle = h.mgr.stream(channelId, 'WatchEvents', new Uint8Array())
    handle.onError(errorFn)

    h.mockWs.simulateClose()

    expect(errorFn).toHaveBeenCalledOnce()
    expect(errorFn.mock.calls[0][0].message).toBe('channel disconnected')
  })

  it('should close all channels when WebSocket closes', async () => {
    const ch1 = await h.openTestChannel('w1')
    const ch2 = await h.openTestChannel('w2')

    h.mockWs.simulateClose()

    expect(h.mgr.isOpen(ch1)).toBe(false)
    expect(h.mgr.isOpen(ch2)).toBe(false)
  })

  // A prior socket's close must not clobber a successor dial started while the
  // prior was CLOSING. The `this.ws === ws` guard skips a close whose successor
  // already OPENED, but a successor still DIALING leaves this.ws pointing at the
  // closing socket while this.wsPromise holds the successor's dial. Nulling
  // wsPromise / clearing the open dedup there orphans the successor's socket and
  // lets a third dial start.
  it('preserves a successor dial when the prior socket closes while still dialing', async () => {
    const wsA = new MockWebSocket()
    const wsB = new MockWebSocket()
    const wsC = new MockWebSocket()
    const queue = [wsA, wsB, wsC]
    let dialCount = 0
    const transport: ChannelTransport = {
      ...createMockTransport(wsA),
      createWebSocket() {
        dialCount++
        return queue.shift()!
      },
    }
    const mgr2 = new ChannelManager(transport, { handshake1: mockHandshake1, handshake2: mockHandshake2 })
    let open2: Promise<string> | undefined
    let open3: Promise<string> | undefined
    try {
      // 1. Open a channel on wsA so this.ws === wsA and its close listener is attached.
      const open1 = mgr2.openChannel('w1')
      await h.flushMicrotasks()
      wsA.simulateOpen()
      await h.flushMicrotasks()
      h.simulatePingAccept(wsA)
      await open1
      expect(dialCount).toBe(1)

      // 2. wsA leaves OPEN but its close event has not fired; a concurrent open dials
      //    wsB as the successor (this.wsPromise = promise_B, this.ws still wsA).
      wsA.readyState = MockWebSocket.CLOSING
      open2 = mgr2.openChannel('w2')
      open2.catch(() => {})
      await h.flushMicrotasks()
      expect(dialCount).toBe(2)
      expect(wsB.readyState).toBe(MockWebSocket.CONNECTING)

      // 3. wsA's queued close finally fires. It must NOT clobber wsB's in-flight dial.
      wsA.simulateClose()

      // 4. A further open must dedup onto wsB's dial, never dial a third socket.
      open3 = mgr2.openChannel('w3')
      open3.catch(() => {})
      await h.flushMicrotasks()
      expect(dialCount).toBe(2)
    }
    finally {
      void open2
      void open3
      mgr2.closeAll()
    }
  })

  // The stale-socket fence covers MESSAGES, not just close: a superseded
  // socket can still deliver frames it buffered before it was replaced, and
  // routing them into the shared channel map would let a stale CLOSE-flag
  // frame drain a live channel (or a stale data frame advance a channel's
  // Noise receive nonce) on the successor's watch.
  it('ignores messages from a superseded socket', async () => {
    const wsA = new MockWebSocket()
    const wsB = new MockWebSocket()
    const queue = [wsA, wsB]
    const transport: ChannelTransport = {
      ...createMockTransport(wsA),
      createWebSocket() {
        return queue.shift()!
      },
    }
    const mgr2 = new ChannelManager(transport, { handshake1: mockHandshake1, handshake2: mockHandshake2 })
    try {
      // 1. Open ch1 on wsA.
      const open1 = mgr2.openChannel('w1')
      await h.flushMicrotasks()
      wsA.simulateOpen()
      await h.flushMicrotasks()
      h.simulatePingAccept(wsA)
      const ch1 = await open1

      // 2. wsA flips CLOSING (its close event still queued); a successor open
      //    dials and installs wsB as the current socket.
      wsA.readyState = MockWebSocket.CLOSING
      const open2 = mgr2.openChannel('w2')
      await h.flushMicrotasks()
      wsB.simulateOpen()
      await h.flushMicrotasks()
      h.simulatePingAccept(wsB)
      await open2

      // 3. wsA delivers a buffered CLOSE-flag frame for ch1. It must be
      //    ignored: ch1 lives in the shared channel map the successor now
      //    serves, and only the CURRENT socket may mutate it.
      wsA.simulateMessage(encodeCloseMessage(ch1))
      expect(mgr2.isOpen(ch1)).toBe(true)
    }
    finally {
      mgr2.closeAll()
    }
  })
})

describe('channelManager closeAll', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should close all channels and the WebSocket', async () => {
    const ch1 = await h.openTestChannel('w1')
    const ch2 = await h.openTestChannel('w2')

    h.mgr.closeAll()

    expect(h.mgr.isOpen(ch1)).toBe(false)
    expect(h.mgr.isOpen(ch2)).toBe(false)
    expect(h.mockWs.readyState).toBe(MockWebSocket.CLOSED)
  })

  // closeAll snapshots this.channels, so an open still parked on an await when
  // the snapshot was taken would register AFTER it and slip past the eager
  // identity release -- the TOCTOU the closeGeneration guard closes. The lazy
  // staleReason re-check prevents cross-user REUSE either way; this pins that
  // the leaked channel itself must not survive.
  it('does not leave a channel registered when closeAll races an open past its snapshot', async () => {
    const openPromise = h.mgr.openChannel('w1')
    await h.flushMicrotasks() // parked at ensureWebSocket, before channels.set
    h.mgr.closeAll() // eager identity release; snapshot misses the unregistered channel
    h.mockWs.simulateOpen()
    await h.flushMicrotasks()
    if (h.mockWs.sent.length > 0) // without the guard the open sent + must answer its verify ping
      h.simulatePingAccept()
    await expect(openPromise).rejects.toThrow(ChannelError) // without the guard: resolves
    expect(h.mgr.hasOpenChannel('w1')).toBe(false)
  })
})

describe('channelManager webSocket connection failure', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should reject openChannel if WebSocket fails to connect', async () => {
    const openPromise = h.mgr.openChannel('w1')
    await h.flushMicrotasks()
    h.mockWs.simulateError()
    await expect(openPromise).rejects.toThrow('WebSocket connection failed')
  })

  it('should reject openChannel if WebSocket times out', async () => {
    // Use a short real timeout rather than fake timers: bun's fake-timer
    // clock does not reliably fire the ensureWebSocket setTimeout when the
    // open path has several awaits ahead of it.
    const timedMgr = new ChannelManager(createMockTransport(h.mockWs), {
      handshake1: mockHandshake1,
      handshake2: mockHandshake2,
      idleRekeyIntervalMs: 0,
      installWakeListener: false,
      wsOpenTimeoutMs: 50,
    })
    try {
      await expect(timedMgr.openChannel('w1')).rejects.toThrow('WebSocket open timed out after 0s')
    }
    finally {
      timedMgr.closeAll()
    }
  })
})

describe('channelManager observability hooks onStateChange', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should fire after openChannel succeeds', async () => {
    const cb = vi.fn()
    h.mgr.onStateChange(cb)
    await h.openTestChannel('w1')
    expect(cb).toHaveBeenCalledOnce()
  })

  it('should fire after closeChannel', async () => {
    const channelId = await h.openTestChannel('w1')
    const cb = vi.fn()
    h.mgr.onStateChange(cb)
    await h.mgr.closeChannel(channelId)
    expect(cb).toHaveBeenCalledOnce()
  })

  it('should fire on WebSocket close (all channels torn down)', async () => {
    await h.openTestChannel('w1')
    await h.openTestChannel('w2')
    const cb = vi.fn()
    h.mgr.onStateChange(cb)
    h.mockWs.simulateClose()
    expect(cb).toHaveBeenCalledOnce()
  })

  it('should fire on CLOSE sentinel', async () => {
    const channelId = await h.openTestChannel('w1')
    const cb = vi.fn()
    h.mgr.onStateChange(cb)
    h.mockWs.simulateMessage(encodeCloseMessage(channelId))
    expect(cb).toHaveBeenCalledOnce()
  })

  it('should not fire after unsubscribe', async () => {
    const cb = vi.fn()
    const unsub = h.mgr.onStateChange(cb)
    unsub()
    await h.openTestChannel('w1')
    expect(cb).not.toHaveBeenCalled()
  })
})

describe('channelManager observability hooks hasOpenChannel', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should return true when channel is open', async () => {
    await h.openTestChannel('w1')
    expect(h.mgr.hasOpenChannel('w1')).toBe(true)
  })

  it('should return false when no channel exists', () => {
    expect(h.mgr.hasOpenChannel('w1')).toBe(false)
  })

  it('should return false after channel is closed', async () => {
    const channelId = await h.openTestChannel('w1')
    await h.mgr.closeChannel(channelId)
    expect(h.mgr.hasOpenChannel('w1')).toBe(false)
  })
})
