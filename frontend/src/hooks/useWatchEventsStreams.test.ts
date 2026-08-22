import type { WatchEventsResponse } from '~/generated/leapmux/v1/workspace_pb'
import { batch, createMemo, createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType, WatchMode, WatchRejectionReason } from '~/generated/leapmux/v1/workspace_pb'
import { ChannelError } from '~/lib/channel'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

vi.mock('~/components/common/Toast', () => ({
  showWarnToastWithLoggedCause: vi.fn(),
}))

vi.mock('~/api/workerRpc', () => ({
  watchEventsViaChannel: vi.fn(),
  // scheduleReconnect asks the relay whether it has latched a terminal close,
  // so that a caller with no error in hand (onEnd) still parks. Null here is
  // "dialable", which is what every case below assumes.
  channelManager: { fatalCloseInfo: vi.fn(() => null) },
}))

const { channelManager, watchEventsViaChannel } = await import('~/api/workerRpc')
const { showWarnToastWithLoggedCause } = await import('~/components/common/Toast')
const { useWatchEventsStreams } = await import('./useWatchEventsStreams')

interface FakeHandle {
  update: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
  onEvent: (cb: (resp: WatchEventsResponse) => void) => void
  onEnd: (cb: () => void) => void
  onError: (cb: (err: Error) => void) => void
  _emit: (resp: WatchEventsResponse) => void
  _end: () => void
  _error: (err: Error) => void
}

function makeHandle(): FakeHandle {
  let onEvent: ((resp: WatchEventsResponse) => void) | undefined
  let onEnd: (() => void) | undefined
  let onErr: ((err: Error) => void) | undefined
  return {
    update: vi.fn(),
    close: vi.fn(),
    onEvent: (cb) => { onEvent = cb },
    onEnd: (cb) => { onEnd = cb },
    onError: (cb) => { onErr = cb },
    _emit: resp => onEvent?.(resp),
    _end: () => onEnd?.(),
    _error: err => onErr?.(err),
  }
}

describe('useWatchEventsStreams', () => {
  const WS = 'ws-test'
  let handles: FakeHandle[]
  let disposeRoot: (() => void) | undefined

  beforeEach(() => {
    vi.useFakeTimers()
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    handles = []
    disposeRoot?.()
    disposeRoot = undefined
    // Module mocks are shared across the file; without this a toast raised by an
    // earlier test would satisfy a later test's call assertion.
    vi.mocked(showWarnToastWithLoggedCause).mockClear()
    vi.mocked(watchEventsViaChannel).mockReset()
    vi.mocked(watchEventsViaChannel).mockImplementation(async () => {
      const h = makeHandle()
      handles.push(h)
      return h as never
    })
  })

  afterEach(() => {
    disposeRoot?.()
    disposeRoot = undefined
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  async function flush() {
    await Promise.resolve()
    await Promise.resolve()
  }

  function mount(plansFn: () => Map<string, { agents: never[], terminals: never[], terminalResync: Set<string> }>, opts: Partial<{
    onEvent: (workerId: string, resp: unknown) => void
    onWorkerOnline: (workerId: string, online: boolean) => void
    onPromoted: (workerId: string, agentIds: string[]) => void
  }> = {}) {
    const harness = installTestBridge({ workspaceId: WS })
    const stores = createTestTabStores(WS)
    createRoot((dispose) => {
      disposeRoot = dispose
      const plans = createMemo(plansFn)
      useWatchEventsStreams({
        view: stores.view,
        plans,
        onEvent: opts.onEvent ?? (() => {}),
        onWorkerOnline: opts.onWorkerOnline ?? (() => {}),
        onPromoted: opts.onPromoted ?? (() => {}),
      })
    })
    return { harness, stores }
  }

  it('opens one stream per worker', async () => {
    const { harness } = mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
      ['w2', { agents: [{ agentId: 'a2', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]))
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: '2', workerId: 'w2' })
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(2)
  })

  it('plan mode change sends update without re-opening', async () => {
    const harness = installTestBridge({ workspaceId: WS })
    const stores = createTestTabStores(WS)
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    const [mode, setMode] = createSignal(WatchMode.NOTIFY)
    createRoot((dispose) => {
      disposeRoot = dispose
      const plans = createMemo(() => new Map([
        ['w1', { agents: [{ agentId: 'a1', mode: mode() } as never], terminals: [], terminalResync: new Set<string>() }],
      ]))
      useWatchEventsStreams({
        view: stores.view,
        plans,
        onEvent: () => {},
        onWorkerOnline: () => {},
        onPromoted: () => {},
      })
    })
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(1)
    setMode(WatchMode.FULL)
    await flush()
    expect(handles[0]!.update).toHaveBeenCalled()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(1)
  })

  it('transport error marks worker offline and reconnects', async () => {
    const online: boolean[] = []
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
      { onWorkerOnline: (_w, o) => online.push(o) },
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    handles[0]!._error(new ChannelError('transport', 'lost'))
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(online).toContain(false)
    expect(vi.mocked(watchEventsViaChannel).mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  // A fatal close must still run the offline transition. Its only reader is
  // useWorkspaceConnection's cleanup effect -- READY terminals to DISCONNECTED,
  // ACTIVE agents back to INACTIVE, streaming text cleared -- and after a fatal
  // close nothing will ever reconnect and finish a half-streamed message. The
  // TOAST is the only thing suppressed: the relay has latched, so
  // "reconnecting..." would be a lie and the shell's sticky toast already names
  // the real cause.
  it('a fatal relay close marks the worker offline without a reconnecting toast', async () => {
    const online: boolean[] = []
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
      { onWorkerOnline: (_w, o) => online.push(o) },
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    handles[0]!._error(new ChannelError('transport', 'too many places', { fatal: true }))
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(online).toContain(false)
    expect(showWarnToastWithLoggedCause).not.toHaveBeenCalled()
  })

  // The mobile case this whole gate exists for: the socket dies while the user
  // is in another app, and the redial succeeds the moment they come back. The
  // user must never learn that happened.
  it('says nothing about a drop the first redial repairs', async () => {
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    handles[0]!._error(new ChannelError('transport', 'channel disconnected'))
    await flush()
    expect(showWarnToastWithLoggedCause, 'the loss itself is never announced').not.toHaveBeenCalled()

    // The first redial reopens, which is the whole outage.
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(120_000)
    await flush()
    expect(showWarnToastWithLoggedCause).not.toHaveBeenCalled()
  })

  // An outage that the redials cannot repair still has to reach the user -- the
  // silence above is a grace period, not a mute.
  it('announces an outage once the quiet redials are spent', async () => {
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    vi.mocked(watchEventsViaChannel).mockImplementation(async () => {
      throw new ChannelError('transport', 'channel disconnected')
    })
    handles[0]!._error(new ChannelError('transport', 'channel disconnected'))
    await flush()

    // Redial 1 fails at ~1s: still inside the grace period.
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(showWarnToastWithLoggedCause).not.toHaveBeenCalled()

    // Redial 2 fails at ~2s more. Two quiet retries are spent, so the user
    // finally hears about it.
    await vi.advanceTimersByTimeAsync(2000)
    await flush()
    expect(showWarnToastWithLoggedCause).toHaveBeenCalledTimes(1)
    // The app's own sentence, not the drained channel's. Rendering err.message
    // is what put "channel disconnected" on a user's screen.
    expect(vi.mocked(showWarnToastWithLoggedCause).mock.calls[0]![0]).toContain('Connection to worker lost')
  })

  // The user reported TWO toasts for one drop. The redial loop must not add a
  // third, fourth and fifth as the backoff climbs.
  it('announces a continuing outage only once', async () => {
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    vi.mocked(watchEventsViaChannel).mockImplementation(async () => {
      throw new ChannelError('transport', 'channel disconnected')
    })
    handles[0]!._error(new ChannelError('transport', 'channel disconnected'))
    await flush()
    await vi.advanceTimersByTimeAsync(300_000)
    await flush()
    expect(vi.mocked(watchEventsViaChannel).mock.calls.length, 'it is still redialing').toBeGreaterThan(3)
    expect(showWarnToastWithLoggedCause).toHaveBeenCalledTimes(1)
  })

  // One hub blip drops every worker at once, and the user has one connection.
  it('announces one outage, not one per worker', async () => {
    const { harness } = mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
      ['w2', { agents: [{ agentId: 'a2', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]))
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: '2', workerId: 'w2' })
    await flush()
    expect(handles).toHaveLength(2)
    vi.mocked(watchEventsViaChannel).mockImplementation(async () => {
      throw new ChannelError('transport', 'channel disconnected')
    })
    handles[0]!._error(new ChannelError('transport', 'channel disconnected'))
    handles[1]!._error(new ChannelError('transport', 'channel disconnected'))
    await flush()
    await vi.advanceTimersByTimeAsync(300_000)
    await flush()
    expect(showWarnToastWithLoggedCause).toHaveBeenCalledTimes(1)
  })

  it('keeps the outage latch while a sibling worker is still down', async () => {
    const { harness } = mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
      ['w2', { agents: [{ agentId: 'a2', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]))
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: '2', workerId: 'w2' })
    await flush()
    vi.mocked(watchEventsViaChannel).mockImplementation(async () => {
      throw new ChannelError('transport', 'channel disconnected')
    })
    handles[0]!._error(new ChannelError('transport', 'channel disconnected'))
    handles[1]!._error(new ChannelError('transport', 'channel disconnected'))
    await flush()
    await vi.advanceTimersByTimeAsync(10_000)
    await flush()
    expect(showWarnToastWithLoggedCause).toHaveBeenCalledTimes(1)

    vi.mocked(watchEventsViaChannel).mockImplementation(async (id: string) => {
      if (id === 'w1') {
        const h = makeHandle()
        handles.push(h)
        return h as never
      }
      throw new ChannelError('transport', 'channel disconnected')
    })
    await vi.advanceTimersByTimeAsync(300_000)
    await flush()
    expect(showWarnToastWithLoggedCause).toHaveBeenCalledTimes(1)
  })

  // The latch must die with the outage it described, or a second, genuinely new
  // outage an hour later is announced nowhere.
  it('announces a second outage after the link came back', async () => {
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()

    const failOpen = async () => {
      throw new ChannelError('transport', 'channel disconnected')
    }
    vi.mocked(watchEventsViaChannel).mockImplementation(failOpen as never)
    handles[0]!._error(new ChannelError('transport', 'channel disconnected'))
    await flush()
    await vi.advanceTimersByTimeAsync(10_000)
    await flush()
    expect(showWarnToastWithLoggedCause).toHaveBeenCalledTimes(1)

    // Let it reconnect, and let the reopened stream carry an event -- that is
    // what resets the backoff, so the next outage starts its grace period over.
    vi.mocked(watchEventsViaChannel).mockImplementation(async () => {
      const h = makeHandle()
      handles.push(h)
      return h as never
    })
    await vi.advanceTimersByTimeAsync(60_000)
    await flush()
    const reopened = handles[handles.length - 1]!
    reopened._emit({ event: { case: 'agentEvent', value: {} } } as never)
    await flush()

    vi.mocked(watchEventsViaChannel).mockImplementation(failOpen as never)
    reopened._error(new ChannelError('transport', 'channel disconnected'))
    await flush()
    expect(showWarnToastWithLoggedCause, 'the new outage gets its own grace period').toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(10_000)
    await flush()
    expect(showWarnToastWithLoggedCause).toHaveBeenCalledTimes(2)
  })

  // A failed open used to re-drain itself on the next microtask, because
  // openStream's finally could not tell a plan parked BY the failure path from
  // one that arrived during the open. Every scheduled delay was skipped and the
  // client redialed as fast as the hub could refuse.
  it('honours the backoff between redials instead of looping on microtasks', async () => {
    vi.mocked(watchEventsViaChannel).mockImplementation(async () => {
      throw new ChannelError('transport', 'channel disconnected')
    })
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    // No timer advance at all: the first open is the only one allowed.
    for (let i = 0; i < 50; i++)
      await Promise.resolve()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(1)

    // 1s -> the second, 2s more -> the third. The doubling sequence, not a spin.
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1999)
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(3)
  })

  // Once ChannelRelay latches, every later dial rejects with the same error
  // before it reaches the network -- so a reconnect timer here is a wakeup every
  // 30s for the life of the page that can never succeed.
  it('a fatal stream error arms no reconnect timer', async () => {
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(1)
    handles[0]!._error(new ChannelError('transport', 'too many places', { fatal: true }))
    await vi.advanceTimersByTimeAsync(120_000)
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(1)
  })

  // The closed cycle: a latched relay throws out of the open itself, so the
  // catch arm is the one that used to re-arm the timer that produced the next
  // identical throw.
  it('a fatal open failure does not retry forever', async () => {
    vi.mocked(watchEventsViaChannel).mockImplementation(async () => {
      throw new ChannelError('transport', 'too many places', { fatal: true })
    })
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(120_000)
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(1)
  })

  it('plan changes enqueue synchronously without awaiting channel open', async () => {
    let resolveOpen!: (h: FakeHandle) => void
    vi.mocked(watchEventsViaChannel).mockImplementation(() => new Promise<FakeHandle>((resolve) => {
      resolveOpen = resolve
    }) as never)
    const harness = installTestBridge({ workspaceId: WS })
    const stores = createTestTabStores(WS)
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    const [mode, setMode] = createSignal(WatchMode.NOTIFY)
    createRoot((dispose) => {
      disposeRoot = dispose
      const plans = createMemo(() => new Map([
        ['w1', { agents: [{ agentId: 'a1', mode: mode() } as never], terminals: [], terminalResync: new Set<string>() }],
      ]))
      useWatchEventsStreams({
        view: stores.view,
        plans,
        onEvent: () => {},
        onWorkerOnline: () => {},
        onPromoted: () => {},
      })
    })
    await Promise.resolve()
    setMode(WatchMode.FULL)
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(1)
    expect(handles).toHaveLength(0)
    const h = makeHandle()
    resolveOpen(h)
    await flush()
    // Pending FULL plan drained after open completes — in-place update, no re-open.
    expect(h.update).toHaveBeenCalled()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(1)
  })

  it('coalesces rapid plan changes into one wire update', async () => {
    const harness = installTestBridge({ workspaceId: WS })
    const stores = createTestTabStores(WS)
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    const [mode, setMode] = createSignal(WatchMode.NOTIFY)
    createRoot((dispose) => {
      disposeRoot = dispose
      const plans = createMemo(() => new Map([
        ['w1', { agents: [{ agentId: 'a1', mode: mode() } as never], terminals: [], terminalResync: new Set<string>() }],
      ]))
      useWatchEventsStreams({
        view: stores.view,
        plans,
        onEvent: () => {},
        onWorkerOnline: () => {},
        onPromoted: () => {},
      })
    })
    await flush()
    handles[0]!.update.mockClear()
    batch(() => {
      setMode(WatchMode.FULL)
      setMode(WatchMode.NOTIFY)
      setMode(WatchMode.FULL)
    })
    await flush()
    expect(handles[0]!.update).toHaveBeenCalledTimes(1)
    expect(handles[0]!.update.mock.calls[0]![0].agents[0].mode).toBe(WatchMode.FULL)
  })

  it('cancels a worker stream when its last tab leaves the plan', async () => {
    const harness = installTestBridge({ workspaceId: WS })
    const stores = createTestTabStores(WS)
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: '2', workerId: 'w2' })
    const [plans, setPlans] = createSignal(new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
      ['w2', { agents: [{ agentId: 'a2', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]))
    createRoot((dispose) => {
      disposeRoot = dispose
      useWatchEventsStreams({
        view: stores.view,
        plans,
        onEvent: () => {},
        onWorkerOnline: () => {},
        onPromoted: () => {},
      })
    })
    await flush()
    expect(handles).toHaveLength(2)
    setPlans(new Map([
      ['w2', { agents: [{ agentId: 'a2', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]))
    await flush()
    expect(handles[0]!.close).toHaveBeenCalled()
    expect(handles[1]!.close).not.toHaveBeenCalled()
  })

  it('retries LOOKUP_FAILED when the tab still exists', async () => {
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    handles[0]!.update.mockClear()
    handles[0]!._emit({
      event: {
        case: 'updateAck',
        value: {
          rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.LOOKUP_FAILED }],
          rejectedTerminals: [],
        },
      },
    } as unknown as WatchEventsResponse)
    await vi.advanceTimersByTimeAsync(500)
    await flush()
    expect(handles[0]!.update).toHaveBeenCalledTimes(1)
  })

  it('does not retry NOT_FOUND even when the tab exists', async () => {
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    handles[0]!.update.mockClear()
    handles[0]!._emit({
      event: {
        case: 'updateAck',
        value: {
          rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.NOT_FOUND }],
          rejectedTerminals: [],
        },
      },
    } as unknown as WatchEventsResponse)
    await vi.advanceTimersByTimeAsync(5000)
    await flush()
    expect(handles[0]!.update).not.toHaveBeenCalled()
  })

  it('does not retry LOOKUP_FAILED when the tab is gone', async () => {
    mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    await flush()
    // No emitAddTab — the plan mentions a1 but no local tab exists.
    handles[0]!.update.mockClear()
    handles[0]!._emit({
      event: {
        case: 'updateAck',
        value: {
          rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.LOOKUP_FAILED }],
          rejectedTerminals: [],
        },
      },
    } as unknown as WatchEventsResponse)
    await vi.advanceTimersByTimeAsync(5000)
    await flush()
    expect(handles[0]!.update).not.toHaveBeenCalled()
  })

  it('stops retrying LOOKUP_FAILED after the retry budget is exhausted', async () => {
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    handles[0]!.update.mockClear()
    // Each ack schedules one retry; wait for it to fire before emitting the next
    // so the pending-timer guard does not collapse the sequence.
    for (let i = 0; i < 12; i++) {
      handles[0]!._emit({
        event: {
          case: 'updateAck',
          value: {
            rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.LOOKUP_FAILED }],
            rejectedTerminals: [],
          },
        },
      } as unknown as WatchEventsResponse)
      await vi.advanceTimersByTimeAsync(20_000)
      await flush()
    }
    // REJECTION_RETRY_MAX is 8.
    expect(handles[0]!.update.mock.calls.length).toBe(8)
  })

  it('calls onPromoted when an agent transitions into FULL', async () => {
    const promoted: Array<{ workerId: string, agentIds: string[] }> = []
    const harness = installTestBridge({ workspaceId: WS })
    const stores = createTestTabStores(WS)
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    const [mode, setMode] = createSignal(WatchMode.NOTIFY)
    createRoot((dispose) => {
      disposeRoot = dispose
      const plans = createMemo(() => new Map([
        ['w1', { agents: [{ agentId: 'a1', mode: mode() } as never], terminals: [], terminalResync: new Set<string>() }],
      ]))
      useWatchEventsStreams({
        view: stores.view,
        plans,
        onEvent: () => {},
        onWorkerOnline: () => {},
        onPromoted: (workerId, agentIds) => promoted.push({ workerId, agentIds }),
      })
    })
    await flush()
    expect(promoted).toEqual([])
    setMode(WatchMode.FULL)
    await flush()
    // Promotion is ack-gated — wire update alone must not fire onPromoted.
    expect(promoted).toEqual([])
    handles[0]!._emit({
      event: {
        case: 'updateAck',
        value: { updateId: 2n, rejectedAgents: [], rejectedTerminals: [] },
      },
    } as unknown as WatchEventsResponse)
    await flush()
    expect(promoted).toEqual([{ workerId: 'w1', agentIds: ['a1'] }])
    setMode(WatchMode.FULL)
    await flush()
    expect(promoted).toHaveLength(1)
  })

  it('marks the worker offline on a clean stream end, then reconnects', async () => {
    const online: boolean[] = []
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
      { onWorkerOnline: (_w, o) => online.push(o) },
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    handles[0]!._end()
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(online).toContain(false)
    expect(vi.mocked(watchEventsViaChannel).mock.calls.length).toBeGreaterThanOrEqual(2)
    expect(online.at(-1)).toBe(true)
  })

  // A stream that ends on its own has no error to hand scheduleReconnect, so a
  // guard reading only that argument let onEnd arm a timer the fatal latch never
  // saw. The timer then woke 30s later purely to have openChannelUncached throw
  // the refusal that was already latched when it was armed.
  it('does not arm a reconnect when the relay has latched, even with no error to pass', async () => {
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    const opensBeforeLatch = vi.mocked(watchEventsViaChannel).mock.calls.length

    // The relay latches, then a worker-side end races the terminal close.
    vi.mocked(channelManager.fatalCloseInfo).mockReturnValue(
      { code: 1008, reason: 'too_many_connections' } as never,
    )
    handles[0]!._end()
    await vi.advanceTimersByTimeAsync(60_000)
    await flush()

    expect(vi.mocked(watchEventsViaChannel).mock.calls.length).toBe(opensBeforeLatch)
  })

  it('resets LOOKUP_FAILED retry budget after a settled updateAck', async () => {
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    handles[0]!.update.mockClear()

    for (let i = 0; i < 3; i++) {
      handles[0]!._emit({
        event: {
          case: 'updateAck',
          value: {
            rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.LOOKUP_FAILED }],
            rejectedTerminals: [],
          },
        },
      } as unknown as WatchEventsResponse)
      await vi.advanceTimersByTimeAsync(20_000)
      await flush()
    }
    expect(handles[0]!.update.mock.calls.length).toBe(3)

    // Settled ack resets the rejection budget.
    handles[0]!._emit({
      event: {
        case: 'updateAck',
        value: { rejectedAgents: [], rejectedTerminals: [] },
      },
    } as unknown as WatchEventsResponse)
    await flush()
    handles[0]!.update.mockClear()

    for (let i = 0; i < 12; i++) {
      handles[0]!._emit({
        event: {
          case: 'updateAck',
          value: {
            rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.LOOKUP_FAILED }],
            rejectedTerminals: [],
          },
        },
      } as unknown as WatchEventsResponse)
      await vi.advanceTimersByTimeAsync(20_000)
      await flush()
    }
    expect(handles[0]!.update.mock.calls.length).toBe(8)
  })

  it('keeps a sibling worker reconnect armed when another worker leaves the plan', async () => {
    const harness = installTestBridge({ workspaceId: WS })
    const stores = createTestTabStores(WS)
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: '2', workerId: 'w2' })
    const [plans, setPlans] = createSignal(new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
      ['w2', { agents: [{ agentId: 'a2', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]))
    createRoot((dispose) => {
      disposeRoot = dispose
      useWatchEventsStreams({
        view: stores.view,
        plans,
        onEvent: () => {},
        onWorkerOnline: () => {},
        onPromoted: () => {},
      })
    })
    await flush()
    expect(handles).toHaveLength(2)
    const opensBefore = vi.mocked(watchEventsViaChannel).mock.calls.length

    // End w1's stream so a reconnect is scheduled; then drop w2 from the plan.
    // Cancelling w2 must not wipe w1's pending reconnect timer.
    handles[0]!._end()
    setPlans(new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]))
    await flush()
    expect(handles[1]!.close).toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(vi.mocked(watchEventsViaChannel).mock.calls.length).toBeGreaterThan(opensBefore)
  })

  it('marks only the failing worker offline on a transport error', async () => {
    const online: Array<{ workerId: string, online: boolean }> = []
    const harness = installTestBridge({ workspaceId: WS })
    const stores = createTestTabStores(WS)
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: '2', workerId: 'w2' })
    createRoot((dispose) => {
      disposeRoot = dispose
      const plans = createMemo(() => new Map([
        ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
        ['w2', { agents: [{ agentId: 'a2', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
      ]))
      useWatchEventsStreams({
        view: stores.view,
        plans,
        onEvent: () => {},
        onWorkerOnline: (workerId, o) => online.push({ workerId, online: o }),
        onPromoted: () => {},
      })
    })
    await flush()
    expect(handles).toHaveLength(2)
    handles[0]!._error(new ChannelError('transport', 'gone'))
    await flush()
    expect(online.filter(e => !e.online)).toEqual([{ workerId: 'w1', online: false }])
    expect(online.some(e => e.workerId === 'w2' && !e.online)).toBe(false)
  })
})
