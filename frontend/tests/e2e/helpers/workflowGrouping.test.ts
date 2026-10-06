/** @vitest-environment jsdom */
import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord, MockModelScenarioStatus } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { RunningNativeChild } from './runningChildProof'
import type { UngroupedChildSlot, UngroupedChildTask } from './workflowGrouping'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider, BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WORKFLOW_TOOL_NAMES } from './providerToolCalls'
import {
  exerciseUngroupedNativeChildren,
  expectNoWorkflowToolOffered,
  expectOpaqueNativeWorkflowResult,
  expectRowsInWorkflowGroup,
  ungroupedChildTaskProblem,
  workflowGroupHeadingElement,
} from './workflowGrouping'

/** The Worker tasks that the fake snapshot read returns. */
const worker = vi.hoisted(() => ({ tasks: [] as unknown[] }))

vi.mock('./nativeSidebarSnapshot', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeSidebarSnapshot')>(),
  readNativeSidebarSnapshot: async () => ({ backgroundTasks: worker.tasks }),
}))

/** A fake registry row with attributes that the fake `expect` reads. */
interface FakeRow {
  fakeRow: true
  attributes: Record<string, string>
}

function isFakeRow(value: unknown): value is FakeRow {
  return typeof value === 'object' && value !== null && 'fakeRow' in value
}

// A fake poll reads once, and a fake row assertion reads the attributes of the row once.
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (isFakeRow(value))
      return { toHaveAttribute: async (name: string, expected: string) => expect(value.attributes[name], message).toBe(expected) }
    return expect(value, message)
  }
  return {
    ...actual,
    expect: Object.assign(check, {
      poll: (read: () => Promise<unknown>, options?: { message?: string }) => ({
        toBe: async (expected: unknown) => expect(await read(), options?.message).toBe(expected),
        toMatch: async (expected: RegExp) => expect(await read(), options?.message).toMatch(expected),
      }),
    }),
  }
})

describe('workflowGroupHeadingElement', () => {
  it('keeps equal heading text in separate groups', () => {
    document.body.innerHTML = `
      <div>goal</div>
      <div id="run" data-testid="bg-task-row"></div>
      <div id="step" data-testid="bg-task-row"></div>
      <div>goal</div>
      <div id="other" data-testid="bg-task-row"></div>
    `
    const run = document.getElementById('run')
    const step = document.getElementById('step')
    const other = document.getElementById('other')
    if (!run || !step || !other)
      throw new Error('the workflow test rows are absent')
    const firstHeading = workflowGroupHeadingElement(run)
    expect(firstHeading?.textContent?.trim()).toBe('goal')
    expect(workflowGroupHeadingElement(step)).toBe(firstHeading)
    expect(workflowGroupHeadingElement(other)).not.toBe(firstHeading)
  })

  it('returns no heading when a row has no preceding element', () => {
    document.body.innerHTML = '<div id="first" data-testid="bg-task-row"></div>'
    const first = document.getElementById('first')
    if (!first)
      throw new Error('the workflow test row is absent')
    expect(workflowGroupHeadingElement(first)).toBeNull()
  })
})

/** A handle of a browser value, as `evaluateHandle` returns it, that runs each evaluation in jsdom. */
interface FakeHandle {
  fakeHandle: true
  value: unknown
  evaluate: (read: (value: unknown, argument: unknown) => unknown, argument?: unknown) => Promise<unknown>
  dispose: () => Promise<void>
}

function handle(value: unknown): FakeHandle {
  return {
    fakeHandle: true,
    value,
    // A handle argument reaches the browser function as its value, as Playwright passes it.
    evaluate: async (read, argument) => read(value, typeof argument === 'object' && argument !== null && 'fakeHandle' in argument ? (argument as FakeHandle).value : argument),
    dispose: async () => {},
  }
}

/** A locator of one jsdom row element. */
function rowLocator(id: string): Locator {
  const element = document.getElementById(id)
  if (!element)
    throw new Error(`The test row ${id} is absent.`)
  return Object.assign({} as Locator, { evaluateHandle: async (read: (row: Element) => unknown) => handle(read(element)) })
}

describe('expectRowsInWorkflowGroup', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <div>leapmux-probe run</div>
      <div id="workflow" data-testid="bg-task-row"></div>
      <div id="child" data-testid="bg-task-row"></div>
      <div>leapmux-probe run</div>
      <div id="other" data-testid="bg-task-row"></div>
    `
  })

  it('accepts rows under one heading that equals the string', async () => {
    await expectRowsInWorkflowGroup([rowLocator('workflow'), rowLocator('child')], 'leapmux-probe run')
  })

  it('accepts rows under one heading that the pattern matches', async () => {
    await expectRowsInWorkflowGroup([rowLocator('workflow'), rowLocator('child')], /leapmux-probe/)
  })

  it('refuses a string that is only part of the heading', async () => {
    await expect(expectRowsInWorkflowGroup([rowLocator('workflow'), rowLocator('child')], 'leapmux-probe')).rejects.toThrow('the workflow group heading of the row')
  })

  it('refuses a row under a second heading with equal text', async () => {
    await expect(expectRowsInWorkflowGroup([rowLocator('workflow'), rowLocator('other')], 'leapmux-probe run')).rejects.toThrow('the row follows the group heading of the first row')
  })

  it.each([[[]], [['workflow']]])('refuses fewer than two rows: %j', async (ids) => {
    await expect(expectRowsInWorkflowGroup(ids.map(rowLocator), 'leapmux-probe run')).rejects.toThrow('two or more rows')
  })
})

describe('expectOpaqueNativeWorkflowResult', () => {
  function context(): ManagedNativeScenarioContext {
    return {
      provider: AgentProvider.CODEWHALE,
      workspaceId: 'workflow-boundary',
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      get page(): Page {
        throw new Error('The assignment boundary must run before browser access.')
      },
      get modelScript(): ModelScript {
        throw new Error('The assignment boundary must run before model access.')
      },
    }
  }

  it.each([{ ruleNames: [] }, { ruleNames: ['same', 'same'] }])('requires distinct native assignment rules before browser access: $ruleNames', async ({ ruleNames }) => {
    await expect(expectOpaqueNativeWorkflowResult(context(), { ruleNames, heading: 'Workflow' })).rejects.toThrow('one or more distinct assignment rules')
  })

  it('accepts one assignment rule and goes on to the browser', async () => {
    await expect(expectOpaqueNativeWorkflowResult(context(), { ruleNames: ['one'], heading: 'Workflow' })).rejects.toThrow('before browser access')
  })
})

describe('expectNoWorkflowToolOffered', () => {
  /** A model script whose recorded requests are `requests`. */
  function script(requests: MockModelRequestRecord[]): Pick<ModelScript, 'status'> {
    const status: MockModelScenarioStatus = { complete: true, nextStep: requests.length, stepCount: requests.length, ruleMatches: {}, pendingGates: [], requests, unexpectedRequests: [] }
    return { status: async () => status }
  }

  function request(tools: string[], stepIndex?: number): MockModelRequestRecord {
    return {
      protocol: 'openai-chat-completions',
      path: '/v1/chat/completions',
      ...(stepIndex === undefined ? { rule: 'a child rule' } : { stepIndex }),
      body: { tools: tools.map(name => ({ type: 'function', function: { name } })) },
    }
  }

  it('accepts a parent request whose catalog offers no workflow tool', async () => {
    await expectNoWorkflowToolOffered(script([request(['Workflow'], undefined), request(['Agent', 'Read'], 0)]))
  })

  it.each(WORKFLOW_TOOL_NAMES)('refuses a parent request that offers %s', async (tool) => {
    await expect(expectNoWorkflowToolOffered(script([request(['Agent', tool], 0)]))).rejects.toThrow('the parent request offers no workflow tool')
  })

  it('reads the first ordered request, not a later one', async () => {
    await expectNoWorkflowToolOffered(script([request(['Agent'], 0), request(['Workflow'], 1)]))
  })

  it('refuses a scenario without an ordered request', async () => {
    await expect(expectNoWorkflowToolOffered(script([request(['Agent'])]))).rejects.toThrow('no parent model request')
  })
})

describe('ungroupedChildTaskProblem', () => {
  function task(overrides: Partial<UngroupedChildTask> = {}): UngroupedChildTask {
    return { kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.COMPLETED, childAgentId: '', groupKey: '', groupLabel: '', ...overrides }
  }
  const first = task({ childAgentId: 'first-child' })
  const second = task({ childAgentId: 'second-child' })

  it('accepts two completed ungrouped children in each identity mode', () => {
    expect(ungroupedChildTaskProblem([first, second], 'distinct')).toBe('')
    expect(ungroupedChildTaskProblem([first, second], ['second-child', 'first-child'])).toBe('')
    expect(ungroupedChildTaskProblem([task(), task()], 'absent')).toBe('')
  })

  it.each([
    ['a workflow task', [first, second, task({ kind: BackgroundTaskKind.WORKFLOW })], 'the registry holds a workflow task'],
    ['a group key', [first, task({ childAgentId: 'second-child', groupKey: 'run-1' })], 'carry a workflow group key or label'],
    ['a group label', [first, task({ childAgentId: 'second-child', groupLabel: 'Run' })], 'carry a workflow group key or label'],
    ['one child', [first], 'holds 1 subagent task(s), not 2'],
    ['three children', [first, second, task({ childAgentId: 'third-child' })], 'holds 3 subagent task(s), not 2'],
    ['a running child', [first, task({ childAgentId: 'second-child', status: BackgroundTaskStatus.RUNNING })], 'a subagent task is not completed'],
  ])('states %s', (_name, tasks, problem) => {
    expect(ungroupedChildTaskProblem(tasks, 'distinct')).toContain(problem)
  })

  it('refuses a child agent where none may exist', () => {
    expect(ungroupedChildTaskProblem([first, task()], 'absent')).toContain('holds a child agent')
  })

  it('refuses a missing or shared child agent where each child needs its own', () => {
    expect(ungroupedChildTaskProblem([first, task()], 'distinct')).toContain('holds no child agent')
    expect(ungroupedChildTaskProblem([first, first], 'distinct')).toContain('hold one child agent')
  })

  it('refuses child agents other than the expected two', () => {
    expect(ungroupedChildTaskProblem([first, second], ['first-child', 'another-child'])).toContain('not ["first-child","another-child"]')
  })
})

describe('exerciseUngroupedNativeChildren', () => {
  it('opens the two slots in order, refuses two children with one agent, and finishes both', async () => {
    const log: string[] = []
    worker.tasks = [{ kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.RUNNING, childAgentId: 'one-child', groupKey: '', groupLabel: '' }]
    const context = Object.assign({} as ManagedNativeScenarioContext, { provider: AgentProvider.KILO })
    const openChild = async (slot: UngroupedChildSlot): Promise<RunningNativeChild> => {
      log.push(`open ${slot.index} ${slot.allowExistingRows}`)
      const row: FakeRow = { fakeRow: true, attributes: { 'data-status': 'running', 'data-child-agent-id': 'one-child' } }
      return {
        row: row as unknown as Locator,
        childId: 'one-child',
        parentId: 'native-parent',
        finish: async () => {
          log.push(`finish ${slot.index}`)
          row.attributes['data-status'] = 'completed'
        },
      }
    }
    await expect(exerciseUngroupedNativeChildren(context, { openChild })).rejects.toThrow('the two children are distinct agents')
    expect(log).toEqual(['open 0 false', 'finish 0', 'open 1 true', 'finish 1'])
  })
})
