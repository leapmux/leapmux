import type { AgentEvent } from '../../../src/generated/proto/leapmux/v1/watch_pb'
import type { NativePermissionOperationPlan } from './nativePermission'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AgentEventSchema } from '../../../src/generated/proto/leapmux/v1/watch_pb'
import { watchAgentEvents } from './agentEventWatch'
import { exerciseMidTurnBypass } from './nativeMidTurnBypass'
import { createNativePermissionFileWrite } from './nativePermission'
import { currentNativeAgent, nativeTextStep } from './nativeScenario'
import { applyPermissionPreset, expectNoControlBanner, sendMessage, waitForAgentIdle, waitForControlBanner } from './ui'

vi.mock('./agentEventWatch', () => ({
  watchAgentEvents: vi.fn(),
}))
vi.mock('./nativePermission', () => ({
  createNativePermissionFileWrite: vi.fn(),
}))
vi.mock('./nativeScenario', () => ({
  currentNativeAgent: vi.fn(),
  nativeTextStep: vi.fn(),
}))
vi.mock('./ui', () => ({
  applyPermissionPreset: vi.fn(),
  expectNoControlBanner: vi.fn(),
  sendMessage: vi.fn(),
  waitForAgentIdle: vi.fn(),
  waitForControlBanner: vi.fn(),
}))

beforeEach(() => vi.resetAllMocks())

interface PendingInstance {
  requestId: string
  claimToken: string
}

function fixture(after: PendingInstance[] = [
  { requestId: 'pending', claimToken: 'first' },
]) {
  const events: string[] = []
  let frames: AgentEvent[] = []
  const setFrames = (pending: PendingInstance[], ended: boolean) => {
    frames = pending.map(value => create(AgentEventSchema, {
      event: {
        case: 'controlRequest',
        value: { ...value, responseState: ControlResponseState.READY },
      },
    }))
    if (ended) {
      frames.push(create(AgentEventSchema, {
        event: { case: 'turnEnd', value: {} },
      }))
    }
  }
  const cancel = vi.fn()
  const receipts = [
    { receipt: 'first' },
    { receipt: 'second' },
  ]
  const operation = (index: number) => ({
    toolCall: { id: `tool-${index}`, name: 'native-operation', arguments: {} },
    beforeDecision: vi.fn(async () => { events.push(`before-${index}`) }),
    nativeProof: vi.fn(async (request: unknown) => {
      events.push(`proof-${index}`)
      if (request !== receipts[index])
        throw new Error('The helper supplied another tool result.')
    }),
  }) satisfies NativePermissionOperationPlan
  const operations = [
    operation(0),
    operation(1),
  ] as const
  const queue = vi.fn(async () => 4)
  const waitForSteps = vi.fn(async (count: number) => {
    events.push(`steps-${count}`)
    setFrames(count === 5
      ? [
          { requestId: 'pending', claimToken: 'first' },
        ]
      : after, count === 7)
  })
  const requestAt = vi.fn(async (index: number) => receipts[index - 5])
  const context = {
    page: {},
    leapmuxServer: {},
    modelScript: {
      queue,
      waitForSteps,
      requestAt,
      prompt: (text: string) => text,
    },
  } as unknown as ManagedNativeScenarioContext
  vi.mocked(currentNativeAgent).mockResolvedValue({ id: 'agent' } as Awaited<ReturnType<typeof currentNativeAgent>>)
  vi.mocked(nativeTextStep).mockImplementation((_context, text) => ({ text }))
  vi.mocked(watchAgentEvents).mockImplementation(async (_server, _agent, options) => ({
    items: () => frames.map(options.select).filter(value => value !== undefined),
    cancel,
  }))
  vi.mocked(applyPermissionPreset).mockImplementation(async () => {
    events.push('bypass')
  })
  vi.mocked(createNativePermissionFileWrite)
    .mockResolvedValueOnce(operations[0])
    .mockResolvedValueOnce(operations[1])
  return {
    context,
    operations,
    events,
    cancel,
    queue,
    waitForSteps,
    requestAt,
    setFrames,
  }
}

describe('exerciseMidTurnBypass', () => {
  it('checks both unchanged operations before Bypass and proves both result requests in order', async () => {
    const run = fixture()
    await exerciseMidTurnBypass(run.context, run.operations)
    expect(run.events).toEqual([
      'steps-5',
      'before-0',
      'before-1',
      'bypass',
      'steps-7',
      'proof-0',
      'proof-1',
    ])
    expect(run.queue).toHaveBeenCalledWith(
      {
        toolCalls: [run.operations[0].toolCall],
      },
      {
        toolCalls: [run.operations[1].toolCall],
      },
      { text: 'Both native tools ran after Bypass.' },
    )
    expect(run.requestAt.mock.calls).toEqual([
      [5],
      [6],
    ])
    expect(waitForControlBanner).toHaveBeenCalledOnce()
    expect(waitForAgentIdle).toHaveBeenCalledOnce()
    expect(expectNoControlBanner).toHaveBeenCalledOnce()
    expect(run.cancel).toHaveBeenCalledOnce()
  })

  it('creates two default operations when the provider supplies no custom operations', async () => {
    const run = fixture()
    await exerciseMidTurnBypass(run.context)
    expect(createNativePermissionFileWrite).toHaveBeenNthCalledWith(1, run.context, expect.objectContaining({ callId: 'mid-bypass-0' }))
    expect(createNativePermissionFileWrite).toHaveBeenNthCalledWith(2, run.context, expect.objectContaining({ callId: 'mid-bypass-1' }))
    expect(run.events).toContain('proof-1')
  })

  it('accepts a repeated event for the original request instance', async () => {
    const run = fixture([
      { requestId: 'pending', claimToken: 'first' },
      { requestId: 'pending', claimToken: 'first' },
    ])
    await exerciseMidTurnBypass(run.context, run.operations)
    expect(run.cancel).toHaveBeenCalledOnce()
  })

  it.each([
    { requestId: 'later', claimToken: 'second' },
    { requestId: 'pending', claimToken: 'replacement' },
  ])('rejects an additional manual approval instance: $requestId / $claimToken', async (extra) => {
    const run = fixture([
      { requestId: 'pending', claimToken: 'first' },
      extra,
    ])
    await expect(exerciseMidTurnBypass(run.context, run.operations)).rejects.toThrow('later native tools need no manual approval')
    expect(run.cancel).toHaveBeenCalledOnce()
  })

  it('does not select Bypass when an operation changed before permission', async () => {
    const run = fixture()
    run.operations[0].beforeDecision.mockRejectedValueOnce(new Error('The file changed before approval.'))
    await expect(exerciseMidTurnBypass(run.context, run.operations)).rejects.toThrow('The file changed before approval.')
    expect(applyPermissionPreset).not.toHaveBeenCalled()
    expect(run.requestAt).not.toHaveBeenCalled()
    expect(run.cancel).toHaveBeenCalledOnce()
  })

  it('preserves a failed continuation and closes the event watch', async () => {
    const run = fixture()
    run.waitForSteps.mockImplementation(async (count) => {
      if (count === 7)
        throw new Error('The provider did not continue.')
      run.setFrames([
        { requestId: 'pending', claimToken: 'first' },
      ], false)
    })
    await expect(exerciseMidTurnBypass(run.context, run.operations)).rejects.toThrow('The provider did not continue.')
    expect(run.requestAt).not.toHaveBeenCalled()
    expect(run.cancel).toHaveBeenCalledOnce()
  })

  it('waits for its own event stream before it accepts the absence of later requests', async () => {
    const run = fixture()
    const initial = [
      { requestId: 'pending', claimToken: 'first' },
    ]
    run.waitForSteps.mockImplementation(async () => {
      run.setFrames(initial, false)
    })
    vi.useFakeTimers()
    try {
      let settled = false
      const outcome = exerciseMidTurnBypass(run.context, run.operations).then(
        () => { settled = true },
        (error: unknown) => {
          settled = true
          return error
        },
      )
      await vi.advanceTimersByTimeAsync(0)
      const settledBeforeStreamEnd = settled
      run.setFrames([
        ...initial,
        { requestId: 'later', claimToken: 'second' },
      ], true)
      await vi.advanceTimersByTimeAsync(1000)
      const failure = await outcome
      expect(settledBeforeStreamEnd).toBe(false)
      expect(String(failure)).toContain('later native tools need no manual approval')
      expect(run.cancel).toHaveBeenCalledOnce()
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('observes ready control requests and live turn ends', async () => {
    const run = fixture()
    await exerciseMidTurnBypass(run.context, run.operations)
    const select = vi.mocked(watchAgentEvents).mock.calls[0]![2].select
    const ready = create(AgentEventSchema, {
      event: {
        case: 'controlRequest',
        value: { requestId: 'ready', responseState: ControlResponseState.READY },
      },
    })
    const completed = create(AgentEventSchema, {
      event: {
        case: 'controlRequest',
        value: { requestId: 'completed', responseState: ControlResponseState.COMPLETED },
      },
    })
    expect(select(ready)).toEqual({ kind: 'request', request: ready.event.value })
    expect(select(completed)).toBeUndefined()
    expect(select(create(AgentEventSchema))).toBeUndefined()
    expect(select(create(AgentEventSchema, {
      event: { case: 'turnEnd', value: {} },
    }))).toEqual({ kind: 'turnEnd' })
    expect(select(create(AgentEventSchema, {
      replay: { replayId: 1n, agentId: 'agent-1' },
      event: { case: 'turnEnd', value: {} },
    }))).toBeUndefined()
    expect(sendMessage).toHaveBeenCalledOnce()
  })
})
