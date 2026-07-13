import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TauriRelayWebSocket } from './tauriRelaySocket'

const bridgeMocks = vi.hoisted(() => ({
  closeHandler: undefined as ((payload: unknown) => void) | undefined,
  openChannelRelay: vi.fn<(relayId: number) => Promise<void>>(),
  closeChannelRelay: vi.fn<(relayId: number) => Promise<void>>(),
  onEvent: vi.fn(),
  sendChannelMessage: vi.fn(),
}))

vi.mock('~/api/platformBridge', () => ({
  parseRelayClosePayload: (payload: unknown) => {
    const close = payload as { code?: unknown, reason?: unknown, wasClean?: unknown } | null
    return {
      code: typeof close?.code === 'number' ? close.code : 1006,
      reason: typeof close?.reason === 'string' ? close.reason : '',
      wasClean: close?.wasClean === true,
    }
  },
  getCapabilities: () => ({ hubTransport: 'proxy' }),
  isTauriApp: () => true,
  platformBridge: {
    closeChannelRelay: bridgeMocks.closeChannelRelay,
    onEvent: bridgeMocks.onEvent,
    openChannelRelay: bridgeMocks.openChannelRelay,
    sendChannelMessage: bridgeMocks.sendChannelMessage,
  },
}))

describe('tauriRelayWebSocket', () => {
  beforeEach(() => {
    bridgeMocks.closeHandler = undefined
    bridgeMocks.onEvent.mockReset()
    bridgeMocks.onEvent.mockImplementation(async (name: string, handler: (payload: unknown) => void) => {
      if (name === 'channel:close')
        bridgeMocks.closeHandler = handler
      return vi.fn()
    })
    bridgeMocks.openChannelRelay.mockReset()
    bridgeMocks.closeChannelRelay.mockReset()
    // closeChannelRelay is async in platformBridge; default it to a resolved
    // promise so the wrapper's `.catch(...)` on it has a promise to attach to.
    bridgeMocks.closeChannelRelay.mockResolvedValue(undefined)
    bridgeMocks.sendChannelMessage.mockReset()
  })

  // dispatch runs from the raw Tauri `channel:message` callback, outside any
  // promise chain, so a throwing listener must be isolated: it must not escape the
  // callback (surfacing as an uncaught error), must not abort the remaining
  // listeners of that type, and must not skip the once-cleanup for the throwing
  // entry. This mirrors the per-listener isolation channel.ts applies via safeCall.
  it('isolates a throwing event listener from its siblings and its own once-cleanup', async () => {
    let messageHandler: ((payload: unknown) => void) | undefined
    bridgeMocks.onEvent.mockImplementation(async (name: string, handler: (payload: unknown) => void) => {
      if (name === 'channel:message')
        messageHandler = handler
      if (name === 'channel:close')
        bridgeMocks.closeHandler = handler
      return vi.fn()
    })

    const socket = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(messageHandler).toBeDefined())

    const calls: string[] = []
    // The throwing listener is `once`, so its removeEventListener must still run.
    socket.addEventListener('message', () => {
      calls.push('throwing')
      throw new Error('listener boom')
    }, { once: true })
    socket.addEventListener('message', () => {
      calls.push('normal')
    })

    // First frame: the throw must not escape, and the sibling must still run.
    expect(() => messageHandler!('')).not.toThrow()
    expect(calls).toEqual(['throwing', 'normal'])

    // Second frame: the throwing `once` listener was removed despite throwing, so
    // only the surviving normal listener runs.
    expect(() => messageHandler!('')).not.toThrow()
    expect(calls).toEqual(['throwing', 'normal', 'normal'])
  })

  it('does not reopen after a close arrives during relay setup', async () => {
    let finishOpen!: () => void
    bridgeMocks.openChannelRelay.mockReturnValue(new Promise<void>((resolve) => {
      finishOpen = resolve
    }))
    const socket = new TauriRelayWebSocket()
    const closeEvents: CloseEvent[] = []
    socket.onclose = event => closeEvents.push(event)

    await vi.waitFor(() => expect(bridgeMocks.openChannelRelay).toHaveBeenCalledOnce())
    bridgeMocks.closeHandler?.({ code: 1011, reason: 'relay failed', wasClean: false })
    finishOpen()
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(socket.readyState).toBe(WebSocket.CLOSED)
    expect(closeEvents).toHaveLength(1)
    expect(closeEvents[0]).toMatchObject({ code: 1011, reason: 'relay failed', wasClean: false })
  })

  // The Go sidecar's relay is a process-wide singleton that OpenChannelRelay
  // reuses and CloseChannelRelay tears down without regard for who opened it. So
  // a superseded wrapper must NOT close it: otherwise wrapper A's teardown (raced
  // by its own open) lands after successor B adopted the relay and kills B's --
  // and because the sidecar cancels the relay context before emitting, B never
  // even receives a `channel:close`, it just silently wedges with every send
  // failing into dispatchSend's catch.
  it('does not tear down a relay a successor wrapper has adopted', async () => {
    let finishOpenA!: () => void
    bridgeMocks.openChannelRelay.mockReturnValueOnce(new Promise<void>((resolve) => {
      finishOpenA = resolve
    }))
    const socketA = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(bridgeMocks.openChannelRelay).toHaveBeenCalledOnce())

    // The consumer closes A while its open is still in flight. close() defers the
    // teardown behind the send chain, so wait for it to land.
    socketA.close()
    await vi.waitFor(() => expect(bridgeMocks.closeChannelRelay).toHaveBeenCalledTimes(1))

    // ...and immediately reconnects, so B claims the relay.
    bridgeMocks.openChannelRelay.mockResolvedValue(undefined)
    const socketB = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socketB.readyState).toBe(WebSocket.OPEN))

    const closesBeforeStaleGuard = bridgeMocks.closeChannelRelay.mock.calls.length

    // A's in-flight open now resolves; its post-open guard must leave B's relay alone.
    finishOpenA()
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(bridgeMocks.closeChannelRelay).toHaveBeenCalledTimes(closesBeforeStaleGuard)
    expect(socketB.readyState).toBe(WebSocket.OPEN)
  })

  // Each relay RPC must NAME the wrapper it is for.
  //
  // The wrapper-id claim orders the two requests in JS, but the sidecar runs each on
  // its own goroutine with no ordering, so a close dispatched for A can execute after
  // B's open adopted the relay. Carrying the id lets the sidecar ignore that stale
  // close; an id that never leaves JS cannot. This pins the id actually crossing the
  // bridge, and that a successor carries its own distinct one.
  it('names the owning wrapper on every relay rpc', async () => {
    bridgeMocks.openChannelRelay.mockResolvedValue(undefined)

    const socketA = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socketA.readyState).toBe(WebSocket.OPEN))
    const openAId = bridgeMocks.openChannelRelay.mock.calls[0]?.[0]
    expect(typeof openAId).toBe('number')

    socketA.close()
    await vi.waitFor(() => expect(bridgeMocks.closeChannelRelay).toHaveBeenCalledTimes(1))
    expect(bridgeMocks.closeChannelRelay.mock.calls[0]?.[0]).toBe(openAId)

    // A successor must claim under its OWN id, or the sidecar could not tell the two
    // apart and the fence would be meaningless.
    const socketB = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socketB.readyState).toBe(WebSocket.OPEN))
    const openBId = bridgeMocks.openChannelRelay.mock.calls[1]?.[0]
    expect(typeof openBId).toBe('number')
    expect(openBId).not.toBe(openAId)

    socketB.close()
  })

  // A CHAIN of failed successors must not strand the relay either.
  //
  // Handing the claim back to the immediate predecessor is not enough: with A live and
  // B then C both failing, C's predecessor is B — a wrapper that never got a relay and
  // is already CLOSED. Restoring B would leave A unable to close, so nobody ever reaps
  // the sidecar relay. Marking it UNOWNED is the only answer that survives a chain.
  it('does not strand the relay when a chain of successor opens fails', async () => {
    bridgeMocks.openChannelRelay.mockResolvedValueOnce(undefined)
    const socketA = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socketA.readyState).toBe(WebSocket.OPEN))

    bridgeMocks.openChannelRelay.mockRejectedValueOnce(new Error('B failed'))
    const socketB = new TauriRelayWebSocket()
    bridgeMocks.openChannelRelay.mockRejectedValueOnce(new Error('C failed'))
    const socketC = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socketB.readyState).toBe(WebSocket.CLOSED))
    await vi.waitFor(() => expect(socketC.readyState).toBe(WebSocket.CLOSED))

    socketA.close()
    await vi.waitFor(() => expect(bridgeMocks.closeChannelRelay).toHaveBeenCalledTimes(1))
  })

  // A throwing consumer handler must not tear down a healthy relay.
  //
  // Native EventTarget dispatch invokes every listener -- the on* property
  // handler included -- independently, so an application bug in an open
  // handler is logged and contained; it is not a connection failure. Before
  // the on* invocations were isolated, a throwing .onopen aborted the open
  // chain into its .catch, which read the throw as an open failure and reaped
  // the just-installed relay.
  it('stays open and keeps dispatching when a consumer onopen handler throws', async () => {
    bridgeMocks.openChannelRelay.mockResolvedValueOnce(undefined)
    const socket = new TauriRelayWebSocket()
    socket.onopen = () => {
      throw new Error('consumer open handler blew up')
    }
    const openListener = vi.fn()
    socket.addEventListener('open', openListener)
    await vi.waitFor(() => expect(socket.readyState).toBe(WebSocket.OPEN))

    // The throwing property handler did not suppress registered listeners…
    expect(openListener).toHaveBeenCalledOnce()
    // …and the healthy relay was neither reaped nor abandoned.
    expect(bridgeMocks.openChannelRelay).toHaveBeenCalledOnce()
    expect(bridgeMocks.closeChannelRelay).not.toHaveBeenCalled()

    // The wrapper still owns the relay: its own close reaps it normally.
    socket.close()
    await vi.waitFor(() => expect(bridgeMocks.closeChannelRelay).toHaveBeenCalledTimes(1))
  })

  // A successor whose own open REJECTS must hand the ownership claim back, or it
  // permanently disarms the predecessor that is still driving a live relay.
  //
  // The claim is taken synchronously in the constructor, before the open is known to
  // succeed. If a failed open kept it, the predecessor's releaseRelay would find the
  // claim held by the successor (see relayClaim) and decline to close -- leaving the
  // sidecar's relay installed with NO owner: its Hub WebSocket stays open and it
  // emits channel:message frames to no listener until some later OpenChannelRelay
  // reuses a relay whose Noise sessions the frontend already discarded.
  it('hands the relay claim back when a successor open fails', async () => {
    bridgeMocks.openChannelRelay.mockResolvedValueOnce(undefined)
    const socketA = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socketA.readyState).toBe(WebSocket.OPEN))

    // B claims the relay, then its open rejects.
    bridgeMocks.openChannelRelay.mockRejectedValueOnce(new Error('sidecar bridge failed'))
    const socketB = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socketB.readyState).toBe(WebSocket.CLOSED))

    // A still owns the live relay, so its close must actually reap it.
    socketA.close()
    await vi.waitFor(() => expect(bridgeMocks.closeChannelRelay).toHaveBeenCalledTimes(1))
  })

  // A native WebSocket fires BOTH 'error' and 'close' when a connection fails, and
  // ChannelManager.ensureWebSocket drains channels in its close handler -- so the
  // relay wrapper must emit both when its open rejects, not just 'error'.
  it('emits close after error when the relay open fails, like a native WebSocket', async () => {
    bridgeMocks.openChannelRelay.mockRejectedValueOnce(new Error('sidecar bridge failed'))
    const socket = new TauriRelayWebSocket()
    const errors: Event[] = []
    const closes: CloseEvent[] = []
    socket.onerror = event => errors.push(event)
    socket.onclose = event => closes.push(event)

    await vi.waitFor(() => expect(socket.readyState).toBe(WebSocket.CLOSED))
    expect(errors).toHaveLength(1)
    expect(closes).toHaveLength(1)
    expect(closes[0]).toMatchObject({ code: 1006, wasClean: false })
  })

  // The pre-open sibling of the failed-open case above: a close that lands BEFORE
  // the open is even attempted must abandon the wrapper's claim too.
  //
  // handleClose (e.g. a predecessor relay's channel:close event reaching the fresh
  // wrapper's listener) marks the wrapper CLOSED before openChannelRelay runs, so
  // the constructor chain takes its early exit -- no relay was ever installed and
  // nothing later releases the claim. If that exit kept the claim, it would dangle
  // on the dead wrapper and the predecessor still driving the live relay could
  // never reap it (releaseIfClaimable sees a foreign claimant) until some future
  // wrapper happened to claim.
  it('abandons the relay claim when a close lands before the open is attempted', async () => {
    bridgeMocks.openChannelRelay.mockResolvedValueOnce(undefined)
    const socketA = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socketA.readyState).toBe(WebSocket.OPEN))

    // B claims the relay at construction; a close event reaches B's listener
    // before its open is dispatched, so its constructor chain exits pre-open.
    const socketB = new TauriRelayWebSocket()
    bridgeMocks.closeHandler?.({ code: 1006, reason: 'relay died', wasClean: false })
    expect(socketB.readyState).toBe(WebSocket.CLOSED)
    await new Promise(resolve => setTimeout(resolve, 0))
    // B never opened a relay -- only A's open ever reached the bridge.
    expect(bridgeMocks.openChannelRelay).toHaveBeenCalledOnce()

    // A still owns the live relay, so its close must actually reap it.
    socketA.close()
    await vi.waitFor(() => expect(bridgeMocks.closeChannelRelay).toHaveBeenCalledTimes(1))
  })

  // Native WebSocket.close() transmits already-buffered data before closing, and
  // this wrapper adopted that contract when it added pre-OPEN buffering. Tearing
  // the relay down synchronously let an already-dispatched send land on a dead
  // relay and fail into dispatchSend's catch -- the last outbound frame lost with
  // no error surfaced.
  it('flushes queued sends before tearing down the relay', async () => {
    bridgeMocks.openChannelRelay.mockResolvedValue(undefined)
    let finishSend!: () => void
    bridgeMocks.sendChannelMessage.mockReturnValueOnce(new Promise<void>((resolve) => {
      finishSend = resolve
    }))
    const socket = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socket.readyState).toBe(WebSocket.OPEN))

    socket.send(new Uint8Array([1, 2, 3]))
    await vi.waitFor(() => expect(bridgeMocks.sendChannelMessage).toHaveBeenCalledOnce())

    socket.close()
    // The send is still in flight, so the relay must still be alive.
    expect(bridgeMocks.closeChannelRelay).not.toHaveBeenCalled()

    finishSend()
    await vi.waitFor(() => expect(bridgeMocks.closeChannelRelay).toHaveBeenCalledTimes(1))
  })

  it('tears down the relay when close() races the open handshake', async () => {
    let finishOpen!: () => void
    bridgeMocks.openChannelRelay.mockReturnValue(new Promise<void>((resolve) => {
      finishOpen = resolve
    }))
    const socket = new TauriRelayWebSocket()

    await vi.waitFor(() => expect(bridgeMocks.openChannelRelay).toHaveBeenCalledOnce())
    // The consumer closes while the Go sidecar is still opening the relay.
    socket.close()
    // The in-flight open resolves on the Go side, installing a live relay whose
    // owning wrapper is already gone. The post-open guard must tear it down so it
    // is not orphaned emitting frames no listener consumes.
    finishOpen()
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(socket.readyState).toBe(WebSocket.CLOSED)
    // The relay is torn down EXACTLY ONCE even though close()'s deferred release
    // and the post-open guard both race to do it: the once-only guard prevents the
    // second closeChannelRelay (releaseIfClaimable treats an unowned relay as
    // claimable, so without it both calls would fire and double-count the close).
    expect(bridgeMocks.closeChannelRelay).toHaveBeenCalledTimes(1)
  })

  it('forwards the close code and reason to the emitted close event', async () => {
    bridgeMocks.openChannelRelay.mockResolvedValue(undefined)
    const socket = new TauriRelayWebSocket()
    const closeEvents: CloseEvent[] = []
    socket.onclose = event => closeEvents.push(event)
    await vi.waitFor(() => expect(bridgeMocks.openChannelRelay).toHaveBeenCalledOnce())
    await new Promise(resolve => setTimeout(resolve, 0))

    // A caller-initiated close(code, reason) -- ChannelManager passes
    // close(1000, 'closed') -- must surface those, not a hard-coded 1000/''.
    socket.close(1000, 'closed')

    // The state transition is synchronous, but the close event fires on a later
    // microtask (see the async-close test below), so await before asserting it.
    expect(socket.readyState).toBe(WebSocket.CLOSED)
    await vi.waitFor(() => expect(closeEvents).toHaveLength(1))
    expect(closeEvents[0]).toMatchObject({ code: 1000, reason: 'closed', wasClean: true })
  })

  // Native WebSocket.close() fires the close event ASYNCHRONOUSLY. The wrapper
  // must match: ChannelManager.closeWebSocket() calls this.ws.close() and then
  // synchronously nulls this.ws, and handleWebSocketClose is gated on
  // this.ws === ws. A synchronous close event would re-enter that listener
  // while this.ws still points here, passing the stale-socket guard the native
  // async event fails -- clearing the in-flight-open dedup cache mid-closeAll on
  // desktop only. So the event must NOT have fired by the time close() returns.
  it('fires the close event asynchronously, like a native WebSocket', async () => {
    bridgeMocks.openChannelRelay.mockResolvedValue(undefined)
    const socket = new TauriRelayWebSocket()
    const closeEvents: CloseEvent[] = []
    socket.onclose = event => closeEvents.push(event)
    await vi.waitFor(() => expect(socket.readyState).toBe(WebSocket.OPEN))

    socket.close(1000, 'closed')

    // Synchronously after close(): state is CLOSED but the event has not fired.
    expect(socket.readyState).toBe(WebSocket.CLOSED)
    expect(closeEvents).toHaveLength(0)

    // It arrives on the next microtask.
    await vi.waitFor(() => expect(closeEvents).toHaveLength(1))
  })

  it('buffers sends before OPEN and flushes them in order once open', async () => {
    let finishOpen!: () => void
    bridgeMocks.openChannelRelay.mockReturnValue(new Promise<void>((resolve) => {
      finishOpen = resolve
    }))
    const socket = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(bridgeMocks.openChannelRelay).toHaveBeenCalledOnce())

    // Pre-OPEN sends must buffer, not dispatch to a not-yet-open relay.
    expect(socket.readyState).toBe(WebSocket.CONNECTING)
    socket.send(new Uint8Array([1, 2, 3]))
    socket.send(new Uint8Array([4, 5, 6]))
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(bridgeMocks.sendChannelMessage).not.toHaveBeenCalled()

    finishOpen()
    await vi.waitFor(() => expect(socket.readyState).toBe(WebSocket.OPEN))
    await new Promise(resolve => setTimeout(resolve, 0))

    // Both buffered frames flushed, in send order, after OPEN.
    expect(bridgeMocks.sendChannelMessage).toHaveBeenCalledTimes(2)
    expect(bridgeMocks.sendChannelMessage.mock.calls[0][0]).not.toBe('')
    expect(bridgeMocks.sendChannelMessage.mock.calls[1][0]).not.toBe('')
  })

  it('drops sends after close', async () => {
    bridgeMocks.openChannelRelay.mockResolvedValue(undefined)
    const socket = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socket.readyState).toBe(WebSocket.OPEN))
    socket.close()
    expect(socket.readyState).toBe(WebSocket.CLOSED)
    bridgeMocks.sendChannelMessage.mockClear()

    socket.send(new Uint8Array([1]))
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(bridgeMocks.sendChannelMessage).not.toHaveBeenCalled()
  })

  it('attaches a .catch to closeChannelRelay on close so a rejection is handled', async () => {
    bridgeMocks.openChannelRelay.mockResolvedValue(undefined)
    const socket = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socket.readyState).toBe(WebSocket.OPEN))

    // closeChannelRelay rejects (e.g. the sidecar is gone). The wrapper must
    // attach a .catch to the returned promise; without it the rejection escapes
    // the synchronous close() as an unhandled promise rejection. Pre-attach a
    // benign catch so the rejection is never unhandled in either branch, then
    // spy on .catch to assert the wrapper attaches its own handler.
    const rejected = Promise.reject(new Error('relay teardown failed'))
    rejected.catch(() => {})
    const catchSpy = vi.spyOn(rejected, 'catch')
    bridgeMocks.closeChannelRelay.mockReturnValueOnce(rejected)

    socket.close()
    expect(socket.readyState).toBe(WebSocket.CLOSED)
    // close() defers the teardown behind the send chain (see the flush test), so
    // the wrapper attaches its catch on a later microtask.
    await vi.waitFor(() => expect(catchSpy).toHaveBeenCalled())
  })

  // Native EventTarget.addEventListener treats re-registering the same
  // (type, listener) pair as a no-op; a wrapper that pushed a second entry would
  // fire the handler twice per event.
  it('ignores a duplicate (type, listener) registration like a native socket', async () => {
    let messageHandler: ((payload: unknown) => void) | undefined
    bridgeMocks.onEvent.mockImplementation(async (name: string, handler: (payload: unknown) => void) => {
      if (name === 'channel:message')
        messageHandler = handler
      if (name === 'channel:close')
        bridgeMocks.closeHandler = handler
      return vi.fn()
    })
    bridgeMocks.openChannelRelay.mockResolvedValueOnce(undefined)
    const socket = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(messageHandler).toBeDefined())

    const listener = vi.fn()
    socket.addEventListener('message', listener)
    socket.addEventListener('message', listener)
    messageHandler!(btoa('x'))
    expect(listener).toHaveBeenCalledTimes(1)
  })

  // A malformed base64 payload makes atob throw; the decode runs straight off the
  // raw Tauri callback, so an unguarded throw would escape uncaught and skip
  // dispatch. It must instead be dropped with a log, without disturbing the socket.
  it('drops an undecodable channel:message payload instead of throwing', async () => {
    let messageHandler: ((payload: unknown) => void) | undefined
    bridgeMocks.onEvent.mockImplementation(async (name: string, handler: (payload: unknown) => void) => {
      if (name === 'channel:message')
        messageHandler = handler
      if (name === 'channel:close')
        bridgeMocks.closeHandler = handler
      return vi.fn()
    })
    bridgeMocks.openChannelRelay.mockResolvedValueOnce(undefined)
    const socket = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socket.readyState).toBe(WebSocket.OPEN))

    const listener = vi.fn()
    socket.addEventListener('message', listener)
    expect(() => messageHandler!('%%%not-base64%%%')).not.toThrow()
    expect(listener).not.toHaveBeenCalled()
    expect(socket.readyState).toBe(WebSocket.OPEN)

    // A well-formed frame afterwards still flows.
    messageHandler!(btoa('ok'))
    expect(listener).toHaveBeenCalledTimes(1)
  })

  // A sidecar-initiated close is a terminal path like every other, so it must
  // settle the relay claim: the wrapper owned an installed relay the sidecar
  // just declared dead, and leaving the claim pointed at this dead wrapper
  // violates the "exactly one wrapper settles" invariant every sibling path
  // (close(), the catch, the pre-open CLOSED check) upholds.
  it('settles the relay claim on a sidecar-initiated close', async () => {
    bridgeMocks.openChannelRelay.mockResolvedValueOnce(undefined)
    const socket = new TauriRelayWebSocket()
    await vi.waitFor(() => expect(socket.readyState).toBe(WebSocket.OPEN))

    bridgeMocks.closeHandler!({ code: 1006, reason: 'relay read loop failed', wasClean: false })
    expect(socket.readyState).toBe(WebSocket.CLOSED)
    // The claim release names the dead relay so the sidecar can clear its slot.
    await vi.waitFor(() => expect(bridgeMocks.closeChannelRelay).toHaveBeenCalledTimes(1))

    // Already settled: the wrapper's own close() must not release twice.
    socket.close()
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(bridgeMocks.closeChannelRelay).toHaveBeenCalledTimes(1)
  })
})
