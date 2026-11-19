import type { Page } from '@playwright/test'
import type { WatchEventsResponse } from '../../../src/generated/proto/leapmux/v1/watch_pb'
import type { MockModelStep } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  WatchEventsRequestSchema,
  WatchEventsResponseSchema,
  WatchMode,
  WorkerWatchService,
} from '../../../src/generated/proto/leapmux/v1/watch_pb'
import { AGENT_WATCH_UPDATE_ID } from '../helpers/agentEventWatch'
import { exerciseKiloGoal, waitForKiloPromptEnd } from './goalScenario'

const { stream, cancel, selectedAgentTabId } = vi.hoisted(() => ({
  stream: vi.fn(),
  cancel: vi.fn(),
  selectedAgentTabId: vi.fn(),
}))

const firstTurn = vi.hoisted(() => ({
  steps: [] as MockModelStep[],
  events: [] as string[],
  held: undefined as string | undefined,
  setGoal: vi.fn(),
}))

vi.mock('../helpers/goalsAndTodos', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/goalsAndTodos')>(),
  setGoal: firstTurn.setGoal,
}))

vi.mock('../helpers/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../helpers/api')>()
  return {
    ...actual,
    getTestChannel: async () => ({
      getOrOpenChannel: async () => 'kilo-native-channel',
      stream,
    }),
  }
})

vi.mock('../helpers/nativeScenario', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../helpers/nativeScenario')>()
  return { ...actual, selectedAgentTabId }
})

describe('exerciseKiloGoal', () => {
  beforeEach(() => {
    firstTurn.steps = []
    firstTurn.events = []
    firstTurn.held = undefined
    firstTurn.setGoal.mockReset()
    firstTurn.setGoal.mockImplementation(async (_page: Page, _objective: unknown, afterSubmit?: () => Promise<void>) => {
      firstTurn.held = firstTurn.steps[0]?.gate
      if (!firstTurn.held)
        throw new Error('The first model response ended before the active goal was checked.')
      await afterSubmit?.()
      firstTurn.events.push('active checked')
    })
  })

  function firstTurnContext(): ManagedNativeScenarioContext {
    return {
      page: {},
      modelScript: {
        queue: async (...steps: MockModelStep[]) => {
          firstTurn.steps = steps
          return 0
        },
        prompt: (text: string) => text,
        waitForGate: async (gate: string) => {
          expect(firstTurn.held).toBe(gate)
          firstTurn.events.push('held response observed')
        },
        releaseGateIfHeld: async (gate: string) => {
          if (firstTurn.held === gate) {
            firstTurn.held = undefined
            firstTurn.events.push('first response released')
          }
          return true
        },
        waitForSteps: async () => {
          expect(firstTurn.held).toBeUndefined()
          firstTurn.events.push('completion checked')
          throw new Error('The probe stopped after first completion.')
        },
      },
    } as unknown as ManagedNativeScenarioContext
  }

  it('holds the first response until the active goal is checked and releases it before waiting for completion', async () => {
    await expect(exerciseKiloGoal(firstTurnContext())).rejects.toThrow('The probe stopped after first completion.')
    expect(firstTurn.events).toEqual([
      'held response observed',
      'active checked',
      'first response released',
      'completion checked',
    ])
  })

  it('releases the held first response when the active goal check fails', async () => {
    firstTurn.setGoal.mockImplementation(async () => {
      firstTurn.held = firstTurn.steps[0]?.gate
      throw new Error('The active goal check failed.')
    })
    await expect(exerciseKiloGoal(firstTurnContext())).rejects.toThrow('The active goal check failed.')
    expect(firstTurn.held).toBeUndefined()
    expect(firstTurn.events).toEqual(['first response released'])
  })
})

describe('waitForKiloPromptEnd', () => {
  it('rejects an absent authoritative queue snapshot instead of reporting completion', async () => {
    const agentId = 'kilo-native-agent'
    selectedAgentTabId.mockResolvedValue(agentId)
    stream.mockReturnValue({
      onMessage: (listener: (response: WatchEventsResponse) => void) => queueMicrotask(() => {
        listener(create(WatchEventsResponseSchema, {
          event: {
            case: 'updateAck',
            value: {
              updateId: AGENT_WATCH_UPDATE_ID,
              agentStates: [
                { agentId, mode: WatchMode.FULL, replayId: AGENT_WATCH_UPDATE_ID },
              ],
            },
          },
        }))
        listener(create(WatchEventsResponseSchema, {
          event: {
            case: 'agentEvent',
            value: {
              agentId,
              event: { case: 'inputQueueChanged', value: {} },
            },
          },
        }))
      }),
      onError: () => {},
      onEnd: () => {},
      cancel,
    })
    const page = {} as unknown as Page
    const leapmuxServer = { hubUrl: 'http://mock.invalid', adminToken: 'mock-admin', workerId: 'mock-worker' }
    const modelScript = {
      testDeadline: () => Date.now() + 240_000,
    }
    const outcome = await waitForKiloPromptEnd({ page, leapmuxServer, modelScript }).then(
      () => undefined,
      (error: unknown) => error,
    )

    expect(outcome).toBeInstanceOf(Error)
    expect(outcome).toMatchObject({
      message: expect.stringContaining('queue snapshot'),
    })
    expect(selectedAgentTabId).toHaveBeenCalledExactlyOnceWith(page)
    expect(stream).toHaveBeenCalledOnce()
    const request = stream.mock.calls[0]
    expect(request).toBeDefined()
    if (!request)
      throw new Error('The Kilo completion wait did not create its native stream.')
    expect(request[0]).toBe('kilo-native-channel')
    expect(request[1]).toBe(WorkerWatchService.method.watchEvents)
    expect(create(WatchEventsRequestSchema, request[2])).toMatchObject({
      agents: [
        { agentId },
      ],
    })
    expect(cancel).toHaveBeenCalledExactlyOnceWith()
  })

  it('reads no queue when the selected tab has no agent ID', async () => {
    const failure = new Error('The selected agent tab has no agent ID in its data-tab-id attribute.')
    selectedAgentTabId.mockRejectedValue(failure)
    stream.mockClear()
    const page = {} as unknown as Page
    const leapmuxServer = { hubUrl: 'http://mock.invalid', adminToken: 'mock-admin', workerId: 'mock-worker' }
    const modelScript = {
      testDeadline: () => Date.now() + 240_000,
    }

    await expect(waitForKiloPromptEnd({ page, leapmuxServer, modelScript })).rejects.toBe(failure)
    expect(stream).not.toHaveBeenCalled()
  })
})
