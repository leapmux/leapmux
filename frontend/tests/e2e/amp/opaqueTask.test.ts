import type { Locator, Page } from '@playwright/test'
import type { BackgroundTaskItem } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelRequestRecord, MockModelRule, MockModelScenarioStatus } from '../helpers/mockModelScript'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider, BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { exerciseOpaqueAmpTaskLimit } from './opaqueTask'

/** The index that the fake queue gives to the first step of the task. A nonzero index proves the offset. */
const QUEUE_START = 7

type SavedTask = Pick<BackgroundTaskItem, 'kind' | 'status' | 'childAgentId'>

/**
 * The fake model, Worker, and page of one test.
 * The fake row starts as a running row without a child agent. A released gate completes the row and shows the report
 * in the transcript.
 */
const fake = vi.hoisted(() => ({
  log: [] as string[],
  reloaded: false,
  childRule: '',
  childReport: '',
  failFinish: false,
  row: { probe: true as const, count: 1, attributes: {} as Record<string, string> },
  runningTasks: [] as SavedTask[],
  savedTasks: [] as SavedTask[],
}))

/** A locator probe that the fake `expect` reads. */
interface Probe {
  probe: true
  count: number
  attributes: Record<string, string>
}

function isProbe(value: unknown): value is Probe {
  return typeof value === 'object' && value !== null && 'probe' in value
}

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (!isProbe(value))
      return actual.expect(value, message)
    return {
      toBeVisible: async () => actual.expect(value.count, message).toBeGreaterThan(0),
      toHaveAttribute: async (name: string, expected: string) => actual.expect(value.attributes[name], message).toBe(expected),
    }
  }
  return { ...actual, expect: Object.assign(check, actual.expect) }
})

vi.mock('../helpers/nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/nativeScenario')>(),
  currentNativeAgent: async () => ({ id: 'amp-parent', agentSessionId: 'amp-thread' }),
}))

vi.mock('../helpers/nativeSidebarSnapshot', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/nativeSidebarSnapshot')>(),
  readNativeSidebarSnapshot: async (_context: unknown, agentId?: string) => {
    fake.log.push(`snapshot ${agentId ?? ''}`)
    return { backgroundTasks: fake.reloaded ? fake.savedTasks : fake.runningTasks }
  },
}))

vi.mock('../helpers/subagentRegistry', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/subagentRegistry')>(),
  requireRegistryRow: async () => fake.row,
}))

vi.mock('./toolResult', async importOriginal => ({
  ...await importOriginal<typeof import('./toolResult')>(),
  ampToolResultReader: () => async (request: MockModelRequestRecord) => ({ text: String(request.body) }),
}))

vi.mock('../helpers/ui', async importOriginal => ({
  ...await importOriginal<typeof import('../helpers/ui')>(),
  sendMessage: async () => {
    fake.log.push('send')
  },
  tabById: (_page: Page, tabId: string) => ({
    click: async () => {
      fake.log.push(`click ${tabId}`)
    },
  }),
  waitForAgentIdle: async () => {
    fake.log.push('idle')
  },
  messageContents: () => ({
    filter: ({ hasText }: { hasText: string }) => ({
      first: () => ({ probe: true, count: fake.row.attributes['data-status'] === 'succeeded' && hasText === fake.childReport ? 1 : 0, attributes: {} }),
    }),
  }),
  waitForSettingsHydrated: async (_page: Page, groupId: string) => {
    fake.log.push(`hydrated ${groupId}`)
  },
}))

function status(): MockModelScenarioStatus {
  const childRequest: MockModelRequestRecord = { protocol: 'anthropic-messages', path: '/v1/messages', rule: fake.childRule, body: 'AMPREMOTETASK' }
  return { complete: false, nextStep: QUEUE_START + 1, stepCount: QUEUE_START + 2, ruleMatches: {}, pendingGates: [], requests: [childRequest], unexpectedRequests: [] }
}

/** A context whose page and model script are the fakes of this file. */
function fakeContext(): ManagedNativeScenarioContext {
  const page = Object.assign({} as Page, {
    reload: async () => {
      fake.log.push('reload')
      fake.reloaded = true
    },
  })
  const modelScript = Object.assign({} as ModelScript, {
    prompt: (text: string) => text,
    rule: async (...rules: MockModelRule[]) => {
      for (const rule of rules) {
        fake.log.push('rule')
        fake.childRule = rule.name
        const respond: unknown = rule.respond
        if (isObject(respond) && typeof respond.text === 'string')
          fake.childReport = respond.text
      }
    },
    queue: async (...steps: unknown[]) => {
      fake.log.push(`queue ${steps.length}`)
      return QUEUE_START
    },
    waitForGate: async () => {
      fake.log.push('gate')
      return status()
    },
    releaseGateIfHeld: async () => {
      fake.log.push('release')
      fake.row.attributes['data-status'] = 'succeeded'
      return true
    },
    status: async () => status(),
    requestAt: async (stepIndex: number): Promise<MockModelRequestRecord> => {
      fake.log.push(`requestAt ${stepIndex}`)
      if (fake.failFinish)
        throw new Error('The parent request never arrived.')
      return { protocol: 'anthropic-messages', path: '/v1/messages', stepIndex, body: fake.childReport }
    },
  })
  return { page, modelScript, provider: AgentProvider.AMP, providerAgent: { provider: AgentProvider.AMP, prefix: 'native-e2e' }, workspaceId: 'amp-opaque', leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' } }
}

const OPEN_LOG = ['rule', 'queue 2', 'send', 'gate', 'snapshot amp-parent']
const FINISH_LOG = ['release', `requestAt ${QUEUE_START + 1}`, 'click amp-parent', 'idle']
const RELOAD_LOG = ['reload', 'hydrated permissionMode', 'snapshot amp-parent']

describe('exerciseOpaqueAmpTaskLimit', () => {
  beforeEach(() => {
    fake.log = []
    fake.reloaded = false
    fake.childRule = ''
    fake.childReport = ''
    fake.failFinish = false
    fake.row = { probe: true, count: 1, attributes: { 'data-status': 'running', 'data-child-agent-id': '', 'aria-disabled': 'true' } }
    fake.runningTasks = [{ kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.RUNNING, childAgentId: '' }]
    fake.savedTasks = [{ kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.SUCCEEDED, childAgentId: '' }]
  })

  it('runs the proof while the task runs, then finishes the task and proves the saved task after a reload', async () => {
    await exerciseOpaqueAmpTaskLimit(fakeContext(), async (task) => {
      fake.log.push('proof')
      expect(task.parentId).toBe('amp-parent')
      expect(task.row).toBe(fake.row as unknown as Locator)
      expect(task.report).toBe(fake.childReport)
      expect(task.report.startsWith(task.progress)).toBe(true)
      expect(task.childRequest.rule).toBe(fake.childRule)
      expect(fake.row.attributes['data-status']).toBe('running')
    })
    expect(fake.log).toEqual([...OPEN_LOG, 'proof', ...FINISH_LOG, ...RELOAD_LOG])
  })

  it('finishes the task and keeps the proof error when the proof fails', async () => {
    await expect(exerciseOpaqueAmpTaskLimit(fakeContext(), async () => {
      fake.log.push('proof')
      throw new Error('The opaque limit proof failed.')
    })).rejects.toThrow('The opaque limit proof failed.')
    expect(fake.log).toEqual([...OPEN_LOG, 'proof', ...FINISH_LOG])
  })

  it('keeps the proof error and the finish error together', async () => {
    fake.failFinish = true
    const proofError = new Error('The opaque limit proof failed.')
    const result: unknown = await exerciseOpaqueAmpTaskLimit(fakeContext(), async () => {
      throw proofError
    }).then(() => null, error => error)
    expect(result).toBeInstanceOf(AggregateError)
    if (!(result instanceof AggregateError))
      throw new Error('The proof error and the finish error were not kept together.')
    expect(result.errors).toHaveLength(2)
    expect(result.errors[0]).toBe(proofError)
    expect(result.errors[1]).toBeInstanceOf(Error)
    expect(result.errors[1].message).toBe('The parent request never arrived.')
    expect(fake.log).not.toContain('reload')
  })

  it('finishes the task once when the proof finishes it first', async () => {
    await exerciseOpaqueAmpTaskLimit(fakeContext(), async (task) => {
      await task.finish()
    })
    expect(fake.log.filter(entry => entry === 'release')).toHaveLength(1)
    expect(fake.log).toEqual([...OPEN_LOG, ...FINISH_LOG, ...RELOAD_LOG])
  })

  it('fails when the saved task links a child agent', async () => {
    fake.savedTasks = [{ kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.SUCCEEDED, childAgentId: 'amp-child' }]
    // Playwright's `expect` fails with the values that it compared: the child link must be empty.
    await expect(exerciseOpaqueAmpTaskLimit(fakeContext(), async () => {})).rejects.toMatchObject({ matcherResult: { actual: 'amp-child', expected: '' } })
    expect(fake.log).toEqual([...OPEN_LOG, ...FINISH_LOG, ...RELOAD_LOG])
  })

  it('fails when the registry saves more than one task', async () => {
    fake.savedTasks = [...fake.savedTasks, { kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.SUCCEEDED, childAgentId: '' }]
    await expect(exerciseOpaqueAmpTaskLimit(fakeContext(), async () => {})).rejects.toMatchObject({ matcherResult: { name: 'toHaveLength', pass: false } })
    expect(fake.log).toEqual([...OPEN_LOG, ...FINISH_LOG, ...RELOAD_LOG])
  })

  it('releases the gate and runs no proof when the running row links a child agent', async () => {
    fake.row.attributes['data-child-agent-id'] = 'amp-child'
    let proved = false
    await expect(exerciseOpaqueAmpTaskLimit(fakeContext(), async () => {
      proved = true
    })).rejects.toMatchObject({ matcherResult: { actual: 'amp-child', expected: '' } })
    expect(proved).toBe(false)
    expect(fake.log).toEqual(['rule', 'queue 2', 'send', 'gate', 'release'])
  })

  it('releases the gate and runs no proof when the Worker task links a child agent', async () => {
    fake.runningTasks = [{ kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.RUNNING, childAgentId: 'amp-child' }]
    await expect(exerciseOpaqueAmpTaskLimit(fakeContext(), async () => {})).rejects.toMatchObject({ matcherResult: { actual: 'amp-child', expected: '' } })
    expect(fake.log).toEqual([...OPEN_LOG, 'release'])
  })
})
