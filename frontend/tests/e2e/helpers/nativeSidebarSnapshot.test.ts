import type { Page } from '@playwright/test'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { BackgroundTaskKind, ListAgentMessagesResponseSchema, WorkerAgentService } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readNativeSidebarSnapshot, requireNativeSidebarSnapshot } from './nativeSidebarSnapshot'

const worker = vi.hoisted(() => ({
  callWorker: vi.fn(),
  currentNativeAgent: vi.fn(),
}))

vi.mock('./api', () => ({
  getTestChannel: async () => ({ callWorker: worker.callWorker }),
}))

vi.mock('./nativeScenario', () => ({
  currentNativeAgent: worker.currentNativeAgent,
}))

describe('requireNativeSidebarSnapshot', () => {
  it('keeps an authoritative empty snapshot', () => {
    const response = create(ListAgentMessagesResponseSchema, { todos: {}, backgroundTasks: {}, goal: {} })
    expect(requireNativeSidebarSnapshot(response)).toEqual({ todos: [], backgroundTasks: [], goal: undefined })
  })

  it('keeps actual task identity and group fields', () => {
    const response = create(ListAgentMessagesResponseSchema, {
      todos: {},
      goal: {
        goal: { objective: 'Ship it' },
      },
      backgroundTasks: {
        tasks: [
          {
            id: 'task-1',
            kind: BackgroundTaskKind.SUBAGENT,
            childAgentId: 'child-1',
            groupKey: 'workflow-1',
            groupLabel: 'Native workflow',
          },
        ],
      },
    })
    expect(requireNativeSidebarSnapshot(response).goal?.objective).toBe('Ship it')
    expect(requireNativeSidebarSnapshot(response).backgroundTasks[0]).toMatchObject({
      id: 'task-1',
      childAgentId: 'child-1',
      groupKey: 'workflow-1',
      groupLabel: 'Native workflow',
    })
  })

  it.each([
    { todos: {}, backgroundTasks: {} },
    { todos: {}, goal: {} },
    { backgroundTasks: {}, goal: {} },
    {},
  ])('refuses a response that omits a snapshot: %j', (fields) => {
    const response = create(ListAgentMessagesResponseSchema, fields)
    expect(() => requireNativeSidebarSnapshot(response)).toThrow('The Worker did not load the native task, to-do and goal snapshots.')
  })
})

describe('readNativeSidebarSnapshot', () => {
  const context = {
    page: {} as Page,
    leapmuxServer: { hubUrl: 'http://hub.test', adminToken: 'token', workerId: 'worker-1' },
  }

  beforeEach(() => {
    worker.callWorker.mockReset().mockResolvedValue(create(ListAgentMessagesResponseSchema, {
      todos: {
        todos: [
          { content: 'Write the tests' },
        ],
      },
      backgroundTasks: {},
      goal: {},
    }))
    worker.currentNativeAgent.mockReset().mockResolvedValue({ id: 'current-agent' })
  })

  it('reads one message page of the stated agent from its Worker, and returns its snapshots', async () => {
    await expect(readNativeSidebarSnapshot(context, 'stated-agent')).resolves.toMatchObject({
      todos: [
        { content: 'Write the tests' },
      ],
      backgroundTasks: [],
      goal: undefined,
    })
    expect(worker.callWorker).toHaveBeenCalledExactlyOnceWith('worker-1', WorkerAgentService.method.listAgentMessages, { agentId: 'stated-agent', limit: 1 })
    expect(worker.currentNativeAgent).not.toHaveBeenCalled()
  })

  it('reads the current agent when no agent is stated', async () => {
    await readNativeSidebarSnapshot(context)
    expect(worker.callWorker).toHaveBeenCalledExactlyOnceWith('worker-1', WorkerAgentService.method.listAgentMessages, { agentId: 'current-agent', limit: 1 })
  })
})
