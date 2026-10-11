import type { UseWatchEventsStreamsOpts } from './useWatchEventsStreams'
import type { WatchEventsRequest, WatchEventsResponse } from '~/generated/proto/leapmux/v1/workspace_pb'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { batch, createMemo, createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { WatchReplayMode } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentEventSchema, TabType, WatchAgentStateSchema, WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode, WatchRejectionReason } from '~/generated/proto/leapmux/v1/workspace_pb'
import { ChannelError } from '~/lib/channel'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

vi.mock('~/components/common/Toast', () => ({
  showWarnToastWithLoggedCause: vi.fn(),
}))

vi.mock('~/api/workerRpc', () => ({
  watchEventsViaChannel: vi.fn(),
  // Return no fatal relay refusal by default. A fatal-latch test supplies the refusal
  // explicitly.

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
  _requestId: () => bigint
}

function makeHandle(initialId = 0n, opening?: Parameters<typeof watchEventsViaChannel>[1]): FakeHandle {
  let requestId = initialId
  const requests = new Map<bigint, WatchEventsRequest>()
  let registered = new Map<string, { agentId: string, mode: WatchMode, replayId: bigint }>()
  if (opening)
    requests.set(initialId, create(WatchEventsRequestSchema, opening))
  let onEvent: ((resp: WatchEventsResponse) => void) | undefined
  let onEnd: (() => void) | undefined
  let onErr: ((err: Error) => void) | undefined
  return {
    update: vi.fn((request: Parameters<typeof watchEventsViaChannel>[1]) => {
      requestId = request.updateId ?? 0n
      requests.set(requestId, create(WatchEventsRequestSchema, request))
    }),
    close: vi.fn(),
    onEvent: (cb) => { onEvent = cb },
    onEnd: (cb) => { onEnd = cb },
    onError: (cb) => { onErr = cb },
    _emit: (resp) => {
      if (resp.event.case === 'updateAck' && !Object.hasOwn(resp.event.value, 'agentStates')) {
        const ack = resp.event.value
        const applied = requests.get(ack.updateId)
        // Model the Worker registry. A failed agent lookup keeps its prior state.
        if (applied && !ack.rejectedAgents.some(entry => entry.reason === WatchRejectionReason.LOOKUP_FAILED)) {
          const refused = new Set(ack.rejectedAgents.map(entry => entry.entityId))
          registered = new Map(applied.agents.filter(entry => !refused.has(entry.agentId)).map(entry => [entry.agentId, {
            agentId: entry.agentId,
            mode: entry.mode,
            replayId: entry.replayId,
          }]))
        }
        onEvent?.(create(WatchEventsResponseSchema, { event: { case: 'updateAck', value: { ...ack, agentStates: [...registered.values()].map(state => create(WatchAgentStateSchema, state)) } } }))
        return
      }
      onEvent?.(resp)
    },
    _end: () => onEnd?.(),
    _error: err => onErr?.(err),
    _requestId: () => requestId,
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
    // Module mocks are shared across this file. Clear their calls before each case.
    // Otherwise, an earlier toast could satisfy a later case's assertion.
    vi.mocked(showWarnToastWithLoggedCause).mockClear()
    vi.mocked(watchEventsViaChannel).mockReset()
    vi.mocked(watchEventsViaChannel).mockImplementation(async (_workerId, request) => {
      const h = makeHandle(request.updateId, request)
      handles.push(h)
      return h as never
    })
  })

  afterEach(() => {
    disposeRoot?.()
    disposeRoot = undefined
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllEnvs()
  })

  async function flush() {
    await Promise.resolve()
    await Promise.resolve()
  }

  function mount(plansFn: () => Map<string, { agents: never[], terminals: never[], terminalResync: Set<string> }>, opts: Partial<Pick<UseWatchEventsStreamsOpts, 'onEvent' | 'onWorkerOnline' | 'onPromoted' | 'onReplayRequested' | 'onReplayRetired'>> = {}) {
    return createRoot((dispose) => {
      disposeRoot = dispose
      const harness = installTestBridge({ workspaceId: WS })
      const stores = createTestTabStores(WS)
      const plans = createMemo(plansFn)
      useWatchEventsStreams({
        view: stores.view,
        plans,
        onEvent: opts.onEvent ?? (() => {}),
        onWorkerOnline: opts.onWorkerOnline ?? (() => {}),
        onPromoted: opts.onPromoted ?? (() => {}),
        ...(opts.onReplayRequested ? { onReplayRequested: opts.onReplayRequested } : {}),
        ...(opts.onReplayRetired ? { onReplayRetired: opts.onReplayRetired } : {}),
      })
      return { harness, stores, dispose }
    })
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

  it('requests replay ownership before the open reaches the transport', async () => {
    const order: string[] = []
    const requested = vi.fn(() => order.push('requested'))
    vi.mocked(watchEventsViaChannel).mockImplementation(async () => {
      order.push('transport')
      const handle = makeHandle()
      handles.push(handle)
      return handle as never
    })
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL, cursorSeq: 17n, replay: WatchReplayMode.AFTER_CURSOR_OR_NONE } as never], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRequested: requested })
    await flush()
    expect(order).toEqual(['requested', 'transport'])
    const request = vi.mocked(watchEventsViaChannel).mock.calls[0]?.[1]
    const entry = request?.agents?.[0]
    if (!request || !entry)
      throw new Error('The opening request must contain its agent entry.')
    expect(entry.cursorSeq).toBe(17n)
    expect(requested).toHaveBeenCalledWith('w1', request.updateId, [expect.objectContaining({ agentId: 'a1', cursorSeq: entry.cursorSeq, replay: WatchReplayMode.AFTER_CURSOR_OR_NONE })])
  })

  it('requests promotion replay before the update reaches the transport', async () => {
    const order: string[] = []
    const [mode, setMode] = createSignal(WatchMode.NOTIFY)
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: mode() } as never], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRequested: () => order.push('requested') })
    await flush()
    expect(order).toEqual([])
    handles[0]!.update.mockImplementation(() => order.push('transport'))
    setMode(WatchMode.FULL)
    await flush()
    expect(order).toEqual(['requested', 'transport'])
  })

  it('keeps the real in-flight replay when a later request still asks for FULL', async () => {
    const requested = vi.fn()
    const [includeOther, setIncludeOther] = createSignal(false)
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never, ...(includeOther() ? [{ agentId: 'a2', mode: WatchMode.NOTIFY } as never] : [])], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRequested: requested })
    await flush()
    expect(requested).toHaveBeenCalledTimes(1)
    setIncludeOther(true)
    await flush()
    expect(handles[0]!.update).toHaveBeenCalledTimes(1)
    expect(requested).toHaveBeenCalledTimes(1)
  })

  it('carries the first replay identity and fixed cursors through a coalesced FULL request', async () => {
    const requested = vi.fn()
    const [cursor, setCursor] = createSignal(7n)
    const [tail, setTail] = createSignal(6n)
    const [includeOther, setIncludeOther] = createSignal(false)
    mount(() => new Map([
      ['w1', {
        agents: [
          { agentId: 'a1', mode: WatchMode.FULL, replay: WatchReplayMode.AFTER_CURSOR_OR_NONE, cursorSeq: cursor(), windowTailSeq: tail() } as never,
          ...(includeOther() ? [{ agentId: 'a2', mode: WatchMode.NOTIFY } as never] : []),
        ],
        terminals: [],
        terminalResync: new Set<string>(),
      }],
    ]), { onReplayRequested: requested })
    await flush()
    const opening = vi.mocked(watchEventsViaChannel).mock.calls[0]?.[1]
    if (!opening)
      throw new Error('The test requires a transmitted opening request.')
    const first = fromBinary(WatchEventsRequestSchema, toBinary(WatchEventsRequestSchema, create(WatchEventsRequestSchema, opening)))
    expect(first.agents[0]?.replayId).toBe(first.updateId)
    expect(first.agents[0]?.cursorSeq).toBe(7n)
    expect(first.agents[0]?.windowTailSeq).toBe(6n)
    batch(() => {
      setCursor(8n)
      setTail(7n)
    })
    await flush()
    expect(handles[0]!.update).not.toHaveBeenCalled()
    setIncludeOther(true)
    await flush()
    expect(handles[0]!.update).toHaveBeenCalledTimes(1)
    const update = handles[0]!.update.mock.calls[0]?.[0]
    if (!update)
      throw new Error('The changed interest must reach the transport.')
    const latest = fromBinary(WatchEventsRequestSchema, toBinary(WatchEventsRequestSchema, create(WatchEventsRequestSchema, update)))
    expect(latest.updateId).not.toBe(first.updateId)
    expect(latest.agents.find(entry => entry.agentId === 'a1')).toMatchObject({ replayId: first.updateId, cursorSeq: 7n, windowTailSeq: 6n })
    expect(latest.agents.find(entry => entry.agentId === 'a2')?.replayId).toBe(0n)
    expect(requested).toHaveBeenCalledTimes(1)
  })

  it('gives a later FULL lifetime its own transmitted identity when the demotion is skipped', async () => {
    const [mode, setMode] = createSignal(WatchMode.FULL)
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: mode(), cursorSeq: 7n } as never], terminals: [], terminalResync: new Set<string>() }],
    ]))
    await flush()
    const firstId = vi.mocked(watchEventsViaChannel).mock.calls[0]?.[1].updateId
    setMode(WatchMode.NOTIFY)
    await flush()
    setMode(WatchMode.FULL)
    await flush()
    expect(handles[0]!.update).toHaveBeenCalledTimes(2)
    const demoted = fromBinary(WatchEventsRequestSchema, toBinary(WatchEventsRequestSchema, create(WatchEventsRequestSchema, handles[0]!.update.mock.calls[0]?.[0])))
    const resumed = fromBinary(WatchEventsRequestSchema, toBinary(WatchEventsRequestSchema, create(WatchEventsRequestSchema, handles[0]!.update.mock.calls[1]?.[0])))
    expect(demoted.agents[0]?.replayId).toBe(0n)
    expect(resumed.agents[0]?.replayId).toBe(resumed.updateId)
    expect(resumed.agents[0]?.replayId).not.toBe(firstId)
  })

  it('retires a skipped pending replay when the exact later ACK confirms no registration', async () => {
    const retired = vi.fn()
    const [includeOther, setIncludeOther] = createSignal(false)
    const { harness } = mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never, ...(includeOther() ? [{ agentId: 'a2', mode: WatchMode.NOTIFY } as never] : [])], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRetired: retired })
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    const openingId = vi.mocked(watchEventsViaChannel).mock.calls[0]?.[1].updateId
    setIncludeOther(true)
    await flush()
    const latestId = handles[0]!._requestId()
    expect(latestId).not.toBe(openingId)
    handles[0]!._emit(create(WatchEventsResponseSchema, { event: { case: 'updateAck', value: {
      updateId: latestId,
      rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.LOOKUP_FAILED }],
      agentStates: [],
    } } }))
    expect(retired).toHaveBeenCalledWith('w1', openingId, ['a1'], 'rejected')
  })

  it('opens a new replay receipt after reconnect and ignores old handle closure', async () => {
    const requested = vi.fn()
    const retired = vi.fn()
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRequested: requested, onReplayRetired: retired })
    await flush()
    const previous = handles[0]!
    const firstId = vi.mocked(watchEventsViaChannel).mock.calls[0]?.[1].updateId
    previous._end()
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    const secondId = vi.mocked(watchEventsViaChannel).mock.calls[1]?.[1].updateId
    expect(requested).toHaveBeenCalledTimes(2)
    expect(firstId).not.toBe(secondId)
    expect(retired).toHaveBeenCalledWith('w1', firstId, ['a1'], 'closed')
    retired.mockClear()
    previous._end()
    previous._error(new Error('old handle'))
    expect(retired).not.toHaveBeenCalled()
  })

  it.each(['demoted', 'removed'] as const)('retires the exact replay when its agent is %s', async (reason) => {
    const retired = vi.fn()
    const [present, setPresent] = createSignal(true)
    const [mode, setMode] = createSignal(WatchMode.FULL)
    mount(() => new Map([
      ['w1', { agents: present() ? [{ agentId: 'a1', mode: mode() } as never] : [], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRetired: retired })
    await flush()
    const requestId = vi.mocked(watchEventsViaChannel).mock.calls[0]?.[1].updateId
    if (reason === 'demoted')
      setMode(WatchMode.NOTIFY)
    else
      setPresent(false)
    await flush()
    expect(retired).toHaveBeenCalledWith('w1', requestId, ['a1'], reason)
  })

  it('retires a rejected replay request without affecting another agent', async () => {
    const retired = vi.fn()
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never, { agentId: 'a2', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRetired: retired })
    await flush()
    const requestId = vi.mocked(watchEventsViaChannel).mock.calls[0]?.[1].updateId
    handles[0]!._emit({ event: { case: 'updateAck', value: {
      updateId: requestId,
      rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.NOT_FOUND }],
      rejectedTerminals: [],
    } } } as unknown as WatchEventsResponse)
    expect(retired).toHaveBeenCalledWith('w1', requestId, ['a1'], 'rejected')
    expect(retired).toHaveBeenCalledTimes(1)
  })

  it('keeps request IDs unique when a worker leaves and returns', async () => {
    const [present, setPresent] = createSignal(true)
    mount(() => present()
      ? new Map([
          ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
        ])
      : new Map())
    await flush()
    const firstId = vi.mocked(watchEventsViaChannel).mock.calls[0]?.[1].updateId
    setPresent(false)
    await flush()
    setPresent(true)
    await flush()
    const secondId = vi.mocked(watchEventsViaChannel).mock.calls[1]?.[1].updateId
    expect(firstId).not.toBe(secondId)
  })

  it.each([2n, 9007199254740993n])('ignores an acknowledgment for unsent request %s', async (updateId) => {
    const promoted = vi.fn()
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]), { onPromoted: promoted })
    await flush()
    handles[0]!._emit({ event: { case: 'updateAck', value: { updateId, rejectedAgents: [], rejectedTerminals: [] } } } as unknown as WatchEventsResponse)
    expect(promoted).not.toHaveBeenCalled()
  })

  it('copies each requested replay entry before the transport runs', async () => {
    const entry = { agentId: 'a1', mode: WatchMode.FULL, cursorSeq: 17n } as never
    const requested = vi.fn()
    mount(() => new Map([
      ['w1', { agents: [entry], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRequested: requested })
    await flush()
    const transmitted = vi.mocked(watchEventsViaChannel).mock.calls[0]?.[1]
    const copied = requested.mock.calls[0]?.[2]
    expect(copied).toEqual(transmitted?.agents)
    expect(copied).not.toBe(transmitted?.agents)
    expect(copied?.[0]).not.toBe(entry)
  })

  it('retires a failed open before another request can begin', async () => {
    const order: string[] = []
    const requested = vi.fn<NonNullable<UseWatchEventsStreamsOpts['onReplayRequested']>>(() => {
      order.push('requested')
    })
    const retired = vi.fn(() => order.push('retired'))
    vi.mocked(watchEventsViaChannel).mockRejectedValueOnce(new Error('open refused'))
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRequested: requested, onReplayRetired: retired })
    await flush()
    const firstId = vi.mocked(watchEventsViaChannel).mock.calls[0]?.[1].updateId
    expect(retired).toHaveBeenCalledWith('w1', firstId, ['a1'], 'closed')
    expect(order).toEqual(['requested', 'retired'])
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(order).toEqual(['requested', 'retired', 'requested'])
    expect(requested.mock.calls[1]?.[1]).not.toBe(firstId)
  })

  it('retires only the new promotion when an update send fails', async () => {
    const requested = vi.fn()
    const retired = vi.fn()
    const [mode, setMode] = createSignal(WatchMode.NOTIFY)
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never, { agentId: 'a2', mode: mode() } as never], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRequested: requested, onReplayRetired: retired })
    await flush()
    const handle = handles[0]!
    handle.update.mockImplementationOnce(() => {
      throw new Error('The update failed.')
    })
    setMode(WatchMode.FULL)
    await flush()
    expect(retired).toHaveBeenCalledTimes(1)
    const promotionId = requested.mock.calls[1]?.[1]
    expect(retired).toHaveBeenCalledWith('w1', promotionId, ['a2'], 'rejected')
    expect(requested.mock.calls[0]?.[2]).toEqual([expect.objectContaining({ agentId: 'a1' })])
    expect(requested.mock.calls[2]?.[2]).toEqual([expect.objectContaining({ agentId: 'a2' })])
    expect(requested.mock.calls[2]?.[1]).not.toBe(promotionId)
  })

  it('keeps an earlier replay when another request rejects an unchanged FULL entry', async () => {
    const requested = vi.fn()
    const retired = vi.fn()
    const onEvent = vi.fn()
    const [includeOther, setIncludeOther] = createSignal(false)
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never, ...(includeOther() ? [{ agentId: 'a2', mode: WatchMode.NOTIFY } as never] : [])], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRequested: requested, onReplayRetired: retired, onEvent })
    await flush()
    const replayId = handles[0]!._requestId()
    const completed = create(WatchEventsResponseSchema, { event: { case: 'agentEvent', value: create(AgentEventSchema, {
      agentId: 'a1',
      replay: true,
      replayId,
      event: { case: 'catchUpComplete', value: { latestSeq: 0n } },
      replayAgentId: 'a1',
    }) } })
    handles[0]!._emit(completed)
    expect(onEvent).toHaveBeenCalledWith('w1', completed)
    setIncludeOther(true)
    await flush()
    handles[0]!._emit({ event: { case: 'updateAck', value: {
      updateId: handles[0]!._requestId(),
      rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.NOT_FOUND }],
      rejectedTerminals: [],
    } } } as unknown as WatchEventsResponse)
    expect(requested).toHaveBeenCalledTimes(1)
    expect(retired).not.toHaveBeenCalled()
    // NOT_FOUND removes the worker's registration. A later real revision promotes again.
    setIncludeOther(false)
    await flush()
    expect(requested).toHaveBeenCalledTimes(2)
    expect(requested).toHaveBeenLastCalledWith('w1', handles[0]!._requestId(), [expect.objectContaining({ agentId: 'a1' })])
  })

  it.each([
    { label: 'the exact origin', origin: 'a1', wrongLifetime: false, completed: true },
    { label: 'an empty origin', origin: '', wrongLifetime: false, completed: false },
    { label: 'another origin', origin: 'a2', wrongLifetime: false, completed: false },
    { label: 'another lifetime', origin: 'a1', wrongLifetime: true, completed: false },
  ])('accepts completion only from its current FULL origin: $label', async ({ origin, wrongLifetime, completed }) => {
    const retired = vi.fn()
    const [includeOther, setIncludeOther] = createSignal(false)
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never, ...(includeOther() ? [{ agentId: 'a2', mode: WatchMode.NOTIFY } as never] : [])], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRetired: retired })
    await flush()
    const handle = handles[0]!
    const replayId = handle._requestId()
    handle._emit(create(WatchEventsResponseSchema, { event: { case: 'agentEvent', value: create(AgentEventSchema, {
      agentId: 'a1',
      replay: true,
      replayId: wrongLifetime ? replayId + 1n : replayId,
      event: { case: 'catchUpComplete', value: { latestSeq: 0n } },
      replayAgentId: origin,
    }) } }))
    setIncludeOther(true)
    await flush()
    handle._emit(create(WatchEventsResponseSchema, { event: { case: 'updateAck', value: {
      updateId: handle._requestId(),
      rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.NOT_FOUND }],
      agentStates: [],
    } } }))
    if (completed)
      expect(retired).not.toHaveBeenCalled()
    else
      expect(retired).toHaveBeenCalledExactlyOnceWith('w1', replayId, ['a1'], 'rejected')
  })

  it('groups teardown by each replay request and ignores late open resolution', async () => {
    const requested = vi.fn()
    const retired = vi.fn()
    const [mode, setMode] = createSignal(WatchMode.NOTIFY)
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never, { agentId: 'a2', mode: mode() } as never], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRequested: requested, onReplayRetired: retired })
    await flush()
    setMode(WatchMode.FULL)
    await flush()
    disposeRoot?.()
    disposeRoot = undefined
    expect(retired.mock.calls).toEqual([
      ['w1', requested.mock.calls[0]?.[1], ['a1'], 'closed'],
      ['w1', requested.mock.calls[1]?.[1], ['a2'], 'closed'],
    ])
    expect(handles[0]!.close).toHaveBeenCalledOnce()

    let resolveOpen!: (handle: FakeHandle) => void
    vi.mocked(watchEventsViaChannel).mockImplementationOnce(() => new Promise<FakeHandle>((resolve) => {
      resolveOpen = resolve
    }) as never)
    const second = mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a3', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]), { onReplayRequested: requested, onReplayRetired: retired })
    await flush()
    const lastRequestId = vi.mocked(watchEventsViaChannel).mock.calls.at(-1)?.[1].updateId
    second.dispose()
    disposeRoot = undefined
    const lateHandle = makeHandle()
    resolveOpen(lateHandle)
    await flush()
    expect(retired).toHaveBeenLastCalledWith('w1', lastRequestId, ['a3'], 'closed')
    expect(lateHandle.close).toHaveBeenCalledOnce()
  })

  it('removes each exact receipt when the worker leaves the plan', async () => {
    const retired = vi.fn()
    const [present, setPresent] = createSignal(true)
    mount(() => present()
      ? new Map([
          ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
        ])
      : new Map(), { onReplayRetired: retired })
    await flush()
    const requestId = handles[0]!._requestId()
    setPresent(false)
    await flush()
    expect(retired).toHaveBeenCalledWith('w1', requestId, ['a1'], 'removed')
    expect(retired).toHaveBeenCalledOnce()
  })

  it('ignores a failed old open after the worker leaves and returns', async () => {
    let rejectOldOpen!: (error: unknown) => void
    vi.mocked(watchEventsViaChannel).mockImplementationOnce(() => new Promise<Awaited<ReturnType<typeof watchEventsViaChannel>>>((_resolve, reject) => {
      rejectOldOpen = reject
    }))
    const online = vi.fn()
    const [present, setPresent] = createSignal(true)
    mount(() => present()
      ? new Map([
          ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
        ])
      : new Map(), { onWorkerOnline: online })
    await flush()
    setPresent(false)
    await flush()
    setPresent(true)
    await flush()
    expect(handles).toHaveLength(1)
    expect(online).toHaveBeenCalledWith('w1', true)
    online.mockClear()
    rejectOldOpen(new ChannelError('transport', 'The old open failed.'))
    await flush()
    expect(online).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(2)
    expect(handles[0]!.close).not.toHaveBeenCalled()
  })

  it.each([0n, 9007199254740993n])('keeps the rejection retry cap after invalid settled ACK %s', async (invalidId) => {
    const { harness } = mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]))
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    const handle = handles[0]!
    for (let i = 0; i < 10; i++) {
      handle._emit({ event: { case: 'updateAck', value: {
        updateId: handle._requestId(),
        rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.LOOKUP_FAILED }],
        rejectedTerminals: [],
      } } } as unknown as WatchEventsResponse)
      handle._emit({ event: { case: 'updateAck', value: { updateId: invalidId, rejectedAgents: [], rejectedTerminals: [] } } } as unknown as WatchEventsResponse)
      await vi.advanceTimersByTimeAsync(20_000)
      await flush()
    }
    expect(handle.update).toHaveBeenCalledTimes(8)
  })

  it('ignores events and closure callbacks from a replaced stream', async () => {
    const onEvent = vi.fn()
    const onWorkerOnline = vi.fn()
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }],
    ]), { onEvent, onWorkerOnline })
    await flush()
    const previous = handles[0]
    if (previous === undefined)
      throw new Error('expected a replaced stream handle')
    previous._end()
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(handles).toHaveLength(2)
    const current = handles[1]
    if (current === undefined)
      throw new Error('expected a live stream handle')
    onEvent.mockClear()
    onWorkerOnline.mockClear()
    const response = { event: { case: 'agentEvent', value: { agentId: 'a1' } } } as WatchEventsResponse
    previous._emit(response)
    previous._end()
    previous._error(new ChannelError('transport', 'old stream closed'))
    expect(onEvent).not.toHaveBeenCalled()
    expect(onWorkerOnline).not.toHaveBeenCalled()
    current._emit(response)
    expect(onEvent).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(5000)
    expect(handles).toHaveLength(2)
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

  it.each([WatchMode.FULL, WatchMode.NOTIFY])('keeps mode %s when an opposite change is cancelled before transmission', async (initial) => {
    const opposite = initial === WatchMode.FULL ? WatchMode.NOTIFY : WatchMode.FULL
    const [mode, setMode] = createSignal(initial)
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: mode() } as never], terminals: [], terminalResync: new Set<string>() }],
    ]))
    await flush()
    const handle = handles[0]!
    handle._emit({
      event: { case: 'updateAck', value: { updateId: 1n, rejectedAgents: [], rejectedTerminals: [] } },
    } as unknown as WatchEventsResponse)
    setMode(opposite)
    setMode(initial)
    await flush()
    expect(handle.update).not.toHaveBeenCalled()
    expect(handle.close).not.toHaveBeenCalled()
    expect(watchEventsViaChannel).toHaveBeenCalledOnce()
  })

  it.each([WatchMode.FULL, WatchMode.NOTIFY])('restores mode %s while an opposite revision awaits acknowledgment', async (initial) => {
    const opposite = initial === WatchMode.FULL ? WatchMode.NOTIFY : WatchMode.FULL
    const [mode, setMode] = createSignal(initial)
    mount(() => new Map([
      ['w1', { agents: [{ agentId: 'a1', mode: mode() } as never], terminals: [], terminalResync: new Set<string>() }],
    ]))
    await flush()
    const handle = handles[0]!
    const acknowledge = (updateId: bigint) => handle._emit({
      event: { case: 'updateAck', value: { updateId, rejectedAgents: [], rejectedTerminals: [] } },
    } as unknown as WatchEventsResponse)
    acknowledge(1n)
    setMode(opposite)
    await flush()
    expect(handle.update).toHaveBeenLastCalledWith(expect.objectContaining({
      updateId: 2n,
      agents: [expect.objectContaining({ mode: opposite })],
    }))
    setMode(initial)
    await flush()
    expect(handle.update).toHaveBeenLastCalledWith(expect.objectContaining({
      updateId: 3n,
      agents: [expect.objectContaining({ mode: initial })],
    }))
    acknowledge(2n)
    acknowledge(3n)
    setMode(opposite)
    await flush()
    expect(handle.update).toHaveBeenLastCalledWith(expect.objectContaining({
      updateId: 4n,
      agents: [expect.objectContaining({ mode: opposite })],
    }))
    expect(watchEventsViaChannel).toHaveBeenCalledOnce()
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

  // A fatal close still marks the worker offline and clears live indicators. It schedules no
  // reconnect timer or reconnect toast.

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

  // A brief mobile connection loss can recover on the first reconnect. Show no outage toast when
  // that reconnect succeeds.

  it('says nothing about a drop the first redial repairs', async () => {
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    handles[0]!._error(new ChannelError('transport', 'channel disconnected'))
    await flush()
    expect(showWarnToastWithLoggedCause, 'the loss itself is never announced').not.toHaveBeenCalled()

    // The first reconnect succeeds and ends this outage.
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(watchEventsViaChannel).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(120_000)
    await flush()
    expect(showWarnToastWithLoggedCause).not.toHaveBeenCalled()
  })

  // Report the outage after the quiet reconnect attempts fail.
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

    // The first reconnect fails after about one second. Keep that first failure quiet.
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(showWarnToastWithLoggedCause).not.toHaveBeenCalled()

    // The second reconnect fails after about two more seconds.
    // Both quiet attempts failed, so report the outage.
    await vi.advanceTimersByTimeAsync(2000)
    await flush()
    expect(showWarnToastWithLoggedCause).toHaveBeenCalledTimes(1)
    // Show the caller's connection message. Keep the transport diagnostic in the log.

    expect(vi.mocked(showWarnToastWithLoggedCause).mock.calls[0]![0]).toContain('Connection to worker lost')
  })

  // The end-to-end test waits for this event after each failed reconnect.
  // Each failure permits another attempt to report the same outage.
  it('reports the loss and each failed redial in a dev build, after the announcement decided', async () => {
    vi.stubEnv('LEAPMUX_DEV', '1')
    const reports: unknown[] = []
    const record = (event: Event) => reports.push({
      ...(event as CustomEvent<Record<string, unknown>>).detail,
      announced: vi.mocked(showWarnToastWithLoggedCause).mock.calls.length,
    })
    window.addEventListener('leapmux:watch-events-redial', record)
    try {
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
      await vi.advanceTimersByTimeAsync(1000)
      await flush()
      await vi.advanceTimersByTimeAsync(2000)
      await flush()
      await vi.advanceTimersByTimeAsync(4000)
      await flush()
      expect(reports).toEqual([
        { workerId: 'w1', failures: 1, announced: 0 },
        { workerId: 'w1', failures: 2, announced: 0 },
        { workerId: 'w1', failures: 3, announced: 1 },
        { workerId: 'w1', failures: 4, announced: 1 },
      ])
    }
    finally {
      window.removeEventListener('leapmux:watch-events-redial', record)
    }
  })

  it('reports no redial outside a dev build', async () => {
    vi.stubEnv('LEAPMUX_DEV', '')
    const reports: unknown[] = []
    const record = (event: Event) => reports.push(event)
    window.addEventListener('leapmux:watch-events-redial', record)
    try {
      const { harness } = mount(
        () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [], terminalResync: new Set<string>() }]]),
      )
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
      await flush()
      handles[0]!._error(new ChannelError('transport', 'channel disconnected'))
      await flush()
      expect(reports).toEqual([])
    }
    finally {
      window.removeEventListener('leapmux:watch-events-redial', record)
    }
  })

  // Report the continuing outage with one toast.
  // Later reconnect failures must not add more outage toasts.
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

  // One Hub outage can disconnect several workers. Show one outage toast.
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

  // Clear the toast state when the outage ends. A later outage must report its own toast.
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

    // Deliver an event through the reopened stream. That event resets the reconnect delay and
    // starts a new quiet period for the next outage.

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

  // A failed open must retain its retry delay. A pending plan in finally would instead schedule
  // another immediate drain.

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

    // The second open waits one second. The third waits two more seconds.
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

  // A fatal relay refusal rejects another open before network access. Schedule no repeated
  // reconnect while that refusal remains.

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

  // An open can fail with the fatal relay refusal. The catch branch must not schedule the next
  // reconnect.

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
    // Send the pending FULL plan after the stream opens. Update that stream without opening another stream.
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
          updateId: handles[0]!._requestId(),
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
          updateId: handles[0]!._requestId(),
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
    // The plan includes a1, but no emitAddTab call places its local tab.
    handles[0]!.update.mockClear()
    handles[0]!._emit({
      event: {
        case: 'updateAck',
        value: {
          updateId: handles[0]!._requestId(),
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
    // Each acknowledgement schedules one retry. Wait for that retry before sending the next acknowledgement.
    // Otherwise, the pending timer merges those retries.
    for (let i = 0; i < 12; i++) {
      handles[0]!._emit({
        event: {
          case: 'updateAck',
          value: {
            updateId: handles[0]!._requestId(),
            rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.LOOKUP_FAILED }],
            rejectedTerminals: [],
          },
        },
      } as unknown as WatchEventsResponse)
      await vi.advanceTimersByTimeAsync(20_000)
      await flush()
    }
    // EVENTS_REJECTION_RETRY.maxAttempts is 8.
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
    // Promotion requires the exact acknowledgement. Transmission alone must not call onPromoted.
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

  // onEnd supplies no error argument. Read fatalCloseInfo also so an existing relay refusal
  // prevents another reconnect timer.

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
    const [includeOther, setIncludeOther] = createSignal(false)
    const { harness } = mount(
      () => new Map([['w1', { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never, ...(includeOther() ? [{ agentId: 'a2', mode: WatchMode.NOTIFY } as never] : [])], terminals: [], terminalResync: new Set<string>() }]]),
    )
    emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: '1', workerId: 'w1' })
    await flush()
    handles[0]!.update.mockClear()

    for (let i = 0; i < 3; i++) {
      handles[0]!._emit({
        event: {
          case: 'updateAck',
          value: {
            updateId: handles[0]!._requestId(),
            rejectedAgents: [{ entityId: 'a1', reason: WatchRejectionReason.LOOKUP_FAILED }],
            rejectedTerminals: [],
          },
        },
      } as unknown as WatchEventsResponse)
      await vi.advanceTimersByTimeAsync(20_000)
      await flush()
    }
    expect(handles[0]!.update.mock.calls.length).toBe(3)

    // A settled acknowledgement resets the rejection count.
    handles[0]!._emit({
      event: {
        case: 'updateAck',
        value: { updateId: handles[0]!._requestId(), rejectedAgents: [], rejectedTerminals: [] },
      },
    } as unknown as WatchEventsResponse)
    await flush()
    setIncludeOther(true)
    await flush()
    expect(handles[0]!.update).toHaveBeenCalledTimes(4)
    handles[0]!.update.mockClear()

    for (let i = 0; i < 12; i++) {
      handles[0]!._emit({
        event: {
          case: 'updateAck',
          value: {
            updateId: handles[0]!._requestId(),
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

    // End w1's stream to schedule its reconnect. Then remove w2 from the plan.
    // Cancelling w2 must retain w1's pending reconnect timer.
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
