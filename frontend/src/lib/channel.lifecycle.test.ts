import type { ChannelTransport } from './channel'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { WorkerWatchService } from '../generated/proto/leapmux/v1/watch_pb'
import { ChannelError } from './channel'
import {
  acceptingKeyPins,
  ChannelManagerTestHarness,
  createMockTransport,
  decodeWireMessage,
  encodeCloseMessage,
  encodeWireMessage,
  failChannelSessionWorker,
  mockHandshake1,
  mockHandshake2,
  MockWebSocket,
  newTestChannelManager,
  sessionHostOf,
  settleChannelSessions,
} from './channel.test-support'

describe('ChannelManager closeChannel', () => {
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
    const handle = h.mgr.stream(channelId, WorkerWatchService.method.watchEvents, {})
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
    await h.settle()
    h.mockWs.simulateOpen()
    await h.settle()
    // The Ping is now in flight; the worker has not answered it.
    const channelId = decodeWireMessage(h.mockWs.sent.at(-1)!).channelId
    await h.mgr.closeChannel(channelId)
    await expect(openPromise).rejects.toThrow(ChannelError)
    await expect(openPromise).rejects.toThrow('channel closed')
  })

  it('rejects the pending openChannel when the server sends CLOSE during the open', async () => {
    const openPromise = h.mgr.openChannel('w1')
    await h.settle()
    h.mockWs.simulateOpen()
    await h.settle()
    const channelId = decodeWireMessage(h.mockWs.sent.at(-1)!).channelId
    h.mockWs.simulateMessage(encodeCloseMessage(channelId))
    await expect(openPromise).rejects.toThrow(ChannelError)
    await expect(openPromise).rejects.toThrow('channel closed by server')
  })

  it('rejects the pending openChannel when the WebSocket closes during the open', async () => {
    const openPromise = h.mgr.openChannel('w1')
    await h.settle()
    h.mockWs.simulateOpen()
    await h.settle()
    h.mockWs.simulateClose()
    await expect(openPromise).rejects.toThrow(ChannelError)
    await expect(openPromise).rejects.toThrow('channel disconnected')
  })
})

describe('ChannelManager close notification', () => {
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
    const handle = h.mgr.stream(channelId, WorkerWatchService.method.watchEvents, {})
    handle.onError(errorFn)

    h.mockWs.simulateMessage(encodeCloseMessage(channelId))

    expect(errorFn).toHaveBeenCalledOnce()
    expect(errorFn.mock.calls[0]?.[0].message).toBe('channel closed by server')
  })
})

describe('ChannelManager decrypt failure', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should close the channel on decrypt failure', async () => {
    const channelId = await h.openTestChannel('w1')
    const callPromise = h.mgr.call(channelId, 'Test', new Uint8Array())

    // Send a message with corrupted ciphertext (not encrypted with the correct key/nonce).
    const corruptCiphertext = new Uint8Array(32)
    corruptCiphertext.fill(0xFF)
    const rejected = expect(callPromise).rejects.toThrow(/decrypt a channel message/)
    h.mockWs.simulateMessage(encodeWireMessage(channelId, corruptCiphertext, { id: 1 }))
    await h.settle()

    // The channel should be closed and the pending request rejected.
    await rejected
    expect(h.mgr.isOpen(channelId)).toBe(false)
  })

  it('should close only the affected channel on decrypt failure', async () => {
    const ch1 = await h.openTestChannel('w1')
    const ch2 = await h.openTestChannel('w2')

    // Corrupt ciphertext on ch1.
    const corruptCiphertext = new Uint8Array(32)
    corruptCiphertext.fill(0xFF)
    h.mockWs.simulateMessage(encodeWireMessage(ch1, corruptCiphertext, { id: 1 }))
    await h.settle()

    expect(h.mgr.isOpen(ch1)).toBe(false)
    expect(h.mgr.isOpen(ch2)).toBe(true)
  })

  // A decrypt failure is a session failure, not an orderly close, so the stream's
  // owner sees an error and reconnects.
  it('should error active streams on decrypt failure', async () => {
    const channelId = await h.openTestChannel('w1')
    const endFn = vi.fn()
    const errorFn = vi.fn()
    const handle = h.mgr.stream(channelId, WorkerWatchService.method.watchEvents, {})
    handle.onEnd(endFn)
    handle.onError(errorFn)

    const corruptCiphertext = new Uint8Array(32)
    corruptCiphertext.fill(0xFF)
    h.mockWs.simulateMessage(encodeWireMessage(channelId, corruptCiphertext, { id: 1 }))
    await h.settle()

    expect(errorFn).toHaveBeenCalledOnce()
    const err = errorFn.mock.calls[0]![0] as ChannelError
    expect(err).toBeInstanceOf(ChannelError)
    expect(err.source).toBe('transport')
    expect(err.message).toMatch(/^decrypt a channel message: /)
    expect(endFn).not.toHaveBeenCalled()
    expect(h.mgr.isOpen(channelId)).toBe(false)
  })
})

describe('ChannelManager webSocket close', () => {
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
    const handle = h.mgr.stream(channelId, WorkerWatchService.method.watchEvents, {})
    handle.onError(errorFn)

    h.mockWs.simulateClose()

    expect(errorFn).toHaveBeenCalledOnce()
    expect(errorFn.mock.calls[0]?.[0].message).toBe('channel disconnected')
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
    const mgr2 = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
    })
    let open2: Promise<string> | undefined
    let open3: Promise<string> | undefined
    try {
      // 1. Open a channel on wsA so this.ws === wsA and its close listener is attached.
      const open1 = mgr2.openChannel('w1')
      await h.settle()
      wsA.simulateOpen()
      await h.settle()
      await h.simulatePingAccept(wsA)
      await open1
      expect(dialCount).toBe(1)

      // 2. wsA leaves OPEN but its close event has not fired; a concurrent open dials
      //    wsB as the successor (this.wsPromise = promise_B, this.ws still wsA).
      wsA.readyState = MockWebSocket.CLOSING
      open2 = mgr2.openChannel('w2')
      open2.catch(() => {})
      await h.settle()
      expect(dialCount).toBe(2)
      expect(wsB.readyState).toBe(MockWebSocket.CONNECTING)

      // 3. wsA's queued close finally fires. It must NOT clobber wsB's in-flight dial.
      wsA.simulateClose()

      // 4. A further open must dedup onto wsB's dial, never dial a third socket.
      open3 = mgr2.openChannel('w3')
      open3.catch(() => {})
      await h.settle()
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
    const mgr2 = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2 },
    })
    try {
      // 1. Open ch1 on wsA.
      const open1 = mgr2.openChannel('w1')
      await h.settle()
      wsA.simulateOpen()
      await h.settle()
      await h.simulatePingAccept(wsA)
      const ch1 = await open1

      // 2. wsA flips CLOSING (its close event still queued); a successor open
      //    dials and installs wsB as the current socket.
      wsA.readyState = MockWebSocket.CLOSING
      const open2 = mgr2.openChannel('w2')
      await h.settle()
      wsB.simulateOpen()
      await h.settle()
      await h.simulatePingAccept(wsB)
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

describe('ChannelManager closeAll', () => {
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
    const rejected = expect(openPromise).rejects.toThrow(ChannelError) // without the guard: resolves
    await h.settle() // parked at ensureWebSocket, before channels.set
    h.mgr.closeAll() // eager identity release; snapshot misses the unregistered channel
    h.mockWs.simulateOpen()
    await h.settle()
    if (h.mockWs.sent.length > 0) // without the guard the open sent + must answer its verify ping
      await h.simulatePingAccept()
    await rejected
    expect(h.mgr.hasOpenChannel('w1')).toBe(false)
  })
})

describe('ChannelManager session worker', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('closes every channel with the worker failure when the session worker fails', async () => {
    const channelId = await h.openTestChannel('w1')
    const call = h.mgr.call(channelId, 'Test', new Uint8Array())
    const onError = vi.fn()
    const onEnd = vi.fn()
    const handle = h.mgr.stream(channelId, WorkerWatchService.method.watchEvents, {})
    handle.onError(onError)
    handle.onEnd(onEnd)
    await h.settle()
    const hubClose = vi.spyOn(h.transport, 'closeChannel')
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const rejected = expect(call).rejects.toThrow(/channel session worker failed: boom/)

    failChannelSessionWorker('boom')
    await h.settle()

    await rejected
    expect(onError).toHaveBeenCalledOnce()
    expect(onError.mock.calls[0]![0]).toMatchObject({ source: 'transport', message: 'channel session worker failed: boom' })
    expect(onEnd).not.toHaveBeenCalled()
    expect(h.mgr.isOpen(channelId)).toBe(false)
    expect(hubClose).toHaveBeenCalledExactlyOnceWith(channelId)
    errorSpy.mockRestore()

    // The next open starts a fresh session worker.
    const next = h.mgr.getOrOpenChannel('w1')
    await h.settle()
    await h.simulatePingAccept()
    const reopened = await next
    expect(reopened).not.toBe(channelId)
    expect(h.mgr.isOpen(reopened)).toBe(true)
  })

  // The handshake of the open lived in the lost worker, so the open must not
  // register a channel whose session no worker holds.
  it('fails an open that waits for the WebSocket when the session worker fails, and rolls the Hub back', async () => {
    const hubClose = vi.spyOn(h.transport, 'closeChannel')
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const opened = expect(h.mgr.openChannel('w1')).rejects.toThrow('open channel: superseded by a concurrent closeAll')
    // The handshake finished, and the open waits for the WebSocket.
    await h.settle()

    failChannelSessionWorker('boom')
    h.mockWs.simulateOpen()
    await h.settle()

    await opened
    expect(h.mockWs.sent).toHaveLength(0)
    expect(hubClose).toHaveBeenCalledOnce()
    expect(h.mgr.hasOpenChannel('w1')).toBe(false)
    errorSpy.mockRestore()
  })

  it('shuts the session worker down in closeAll, and starts a fresh one for the next open', async () => {
    const sockets: MockWebSocket[] = []
    const transport: ChannelTransport = {
      ...createMockTransport(h.mockWs),
      createWebSocket() {
        const ws = new MockWebSocket()
        sockets.push(ws)
        return ws
      },
    }
    const mgr = newTestChannelManager(transport, {
      session: { handshake1: mockHandshake1, handshake2: mockHandshake2, idleRekeyIntervalMs: 0 },
      installWakeListener: false,
      keyPins: acceptingKeyPins(),
    })
    /** Open a channel to `workerId` over a freshly dialed socket. */
    const openOnFreshSocket = async (workerId: string) => {
      const opened = mgr.openChannel(workerId)
      await settleChannelSessions()
      const ws = sockets.at(-1)!
      ws.simulateOpen()
      await settleChannelSessions()
      await h.simulatePingAccept(ws)
      return opened
    }
    const host = sessionHostOf(mgr)
    try {
      const first = await openOnFreshSocket('w1')
      const firstEndpoint = host.endpoint()!
      expect(firstEndpoint.sessionState(first)).toBeDefined()

      mgr.closeAll()
      await settleChannelSessions()

      expect(host.inFlight()).toBe(0)
      expect(firstEndpoint.sessionState(first)).toBeUndefined()

      const second = await openOnFreshSocket('w1')
      expect(mgr.isOpen(second)).toBe(true)
      expect(host.endpoint()).not.toBe(firstEndpoint)
      expect(host.endpoint()!.sessionState(second)).toBeDefined()
    }
    finally {
      mgr.closeAll()
    }
  })
})

describe('ChannelManager webSocket connection failure', () => {
  const h = new ChannelManagerTestHarness()
  beforeEach(() => h.setup())
  afterEach(() => h.teardown())

  it('should reject openChannel if WebSocket fails to connect', async () => {
    const openPromise = h.mgr.openChannel('w1')
    await h.settle()
    h.mockWs.simulateError()
    await expect(openPromise).rejects.toThrow('WebSocket connection failed')
  })

  it('should reject openChannel if WebSocket times out', async () => {
    // Use a short real timeout rather than fake timers: bun's fake-timer
    // clock does not reliably fire the ensureWebSocket setTimeout when the
    // open path has several awaits ahead of it.
    const timedMgr = newTestChannelManager(createMockTransport(h.mockWs), {
      session: {
        handshake1: mockHandshake1,
        handshake2: mockHandshake2,
        idleRekeyIntervalMs: 0,
      },
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

describe('ChannelManager observability hooks onStateChange', () => {
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

describe('ChannelManager observability hooks hasOpenChannel', () => {
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
