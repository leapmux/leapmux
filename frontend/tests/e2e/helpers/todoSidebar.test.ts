/**
 * The unit tests check the lists of the to-do replacement scenario and the order of its steps against fakes.
 * The to-do-sidebar browser specs of each provider check the actual sidebar.
 */
import type { Page } from '@playwright/test'
import type { MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { completedTodoList, exerciseTodoListReplacement, firstChangedTodoStep, initialTodoList, TODO_LIST_STEPS } from './todoSidebar'

const fake = vi.hoisted(() => ({ events: [] as string[], queued: [] as MockModelStep[][], counts: {} as Record<string, number> }))

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  sendMessage: async (_page: unknown, text: string) => { fake.events.push(`send ${text}`) },
  waitForAgentIdle: async () => { fake.events.push('idle') },
}))
vi.mock('./goalsAndTodos', () => ({
  goalsAndTodosSection: () => ({ name: 'section' }),
  goalsAndTodosList: () => ({ name: 'list', locator: (selector: string) => ({ name: selector }) }),
  expandGoalsAndTodosSection: async () => { fake.events.push('expand') },
}))
vi.mock('./providerToolCalls', () => ({
  updateTodosToolCall: (_provider: AgentProvider, id: string, steps: unknown) => ({ id, name: 'unit-todo', arguments: { steps } }),
}))
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: (target: { name: string }) => ({
      toBeVisible: async () => { fake.events.push(`visible ${target.name}`) },
      toContainText: async (text: string) => { fake.events.push(`text ${text}`) },
      toHaveCount: async (count: number) => {
        fake.events.push(`count ${target.name}=${count}`)
        // The fake reports the count that the test set for a selector, and the expected count otherwise.
        if (target.name in fake.counts && fake.counts[target.name] !== count)
          throw new Error(`${target.name} holds ${fake.counts[target.name]} items, not ${count}`)
      },
    }),
  }
})

beforeEach(() => {
  fake.events = []
  fake.queued = []
  fake.counts = {}
})

function todoContext(): NativeScenarioContext {
  const page = Object.assign({} as Page, {
    reload: async () => {
      fake.events.push('reload')
      return null
    },
  })
  const modelScript = {
    prompt: (text: string) => text,
    queue: async (...steps: MockModelStep[]) => {
      const start = fake.queued.flat().length
      fake.queued.push(steps)
      fake.events.push(`queue ${steps.length}`)
      return start
    },
    waitForSteps: async (count: number) => { fake.events.push(`steps ${count}`) },
  } as unknown as ModelScript
  return { page, modelScript, provider: AgentProvider.OPENCODE }
}

describe('initialTodoList', () => {
  it('starts three steps completed, in progress, and pending', () => {
    expect(initialTodoList(TODO_LIST_STEPS)).toEqual([
      { step: 'Inspect the repository', status: 'completed' },
      { step: 'List three checks', status: 'in_progress' },
      { step: 'Report their purpose', status: 'pending' },
    ])
  })

  it('starts two steps completed and pending, with no step in progress', () => {
    expect(initialTodoList(['First', 'Last'])).toEqual([{ step: 'First', status: 'completed' }, { step: 'Last', status: 'pending' }])
  })

  it.each([[[]], [['Only']]])('refuses %j, which cannot start completed and pending', (steps) => {
    expect(() => initialTodoList(steps)).toThrow('two or more steps')
  })

  it('refuses an empty step and two steps with one text', () => {
    expect(() => initialTodoList(['First', ' '])).toThrow('needs text')
    expect(() => initialTodoList(['Same', 'Same'])).toThrow('its own text')
  })
})

describe('completedTodoList', () => {
  it('keeps the steps in order and completes each one', () => {
    expect(completedTodoList(['First', 'Middle', 'Last']).map(item => item.status)).toEqual(['completed', 'completed', 'completed'])
    expect(completedTodoList(['First', 'Middle', 'Last']).map(item => item.step)).toEqual(['First', 'Middle', 'Last'])
  })
})

describe('firstChangedTodoStep', () => {
  it('returns the first step of a new list', () => {
    expect(firstChangedTodoStep([], initialTodoList(['First', 'Last']))).toBe('First')
  })

  it('returns the first step whose status changes', () => {
    expect(firstChangedTodoStep(initialTodoList(TODO_LIST_STEPS), completedTodoList(TODO_LIST_STEPS))).toBe('List three checks')
    expect(firstChangedTodoStep(initialTodoList(['First', 'Last']), completedTodoList(['First', 'Last']))).toBe('Last')
  })

  it('refuses a write that changes nothing', () => {
    expect(() => firstChangedTodoStep(completedTodoList(['First', 'Last']), completedTodoList(['First', 'Last']))).toThrow('changes no step')
  })
})

describe('exerciseTodoListReplacement', () => {
  it('writes the first list, checks each step and status, replaces the list, and checks it again after a reload', async () => {
    await exerciseTodoListReplacement(todoContext())
    expect(fake.queued.map(steps => steps.length)).toEqual([2, 2])
    expect(fake.queued[0]?.[0]?.toolCalls?.[0]?.arguments).toEqual({ steps: initialTodoList(TODO_LIST_STEPS) })
    expect(fake.queued[1]?.[0]?.toolCalls?.[0]?.arguments).toEqual({ steps: completedTodoList(TODO_LIST_STEPS) })
    expect(fake.events).toEqual([
      'queue 2',
      'send Write a 3-step to-do list.',
      'steps 2',
      'idle',
      'visible section',
      'expand',
      'text Inspect the repository',
      'text List three checks',
      'text Report their purpose',
      'count [data-task-checkbox="completed"]=1',
      'count [data-task-checkbox="in_progress"]=1',
      'count [data-task-checkbox="pending"]=1',
      'queue 2',
      'send Mark every step done.',
      'steps 4',
      'idle',
      'count [data-task-checkbox="completed"]=3',
      'count [data-task-checkbox="in_progress"]=0',
      'count [data-task-checkbox="pending"]=0',
      'reload',
      'expand',
      'count [data-task-checkbox="completed"]=3',
      'count [data-task-checkbox="in_progress"]=0',
      'count [data-task-checkbox="pending"]=0',
    ])
  })

  it('answers in the step of the tool call for a single-request provider', async () => {
    await exerciseTodoListReplacement(todoContext(), { singleRequest: true })
    expect(fake.queued.map(steps => steps.length)).toEqual([1, 1])
    expect(fake.queued[0]?.[0]).toMatchObject({ text: 'The plan is written.', toolCalls: [{ id: 'todos-first' }] })
    expect(fake.events.filter(event => event.startsWith('steps'))).toEqual(['steps 1', 'steps 2'])
  })

  it('approves each write after its tool step with the first step that the write changes', async () => {
    const approvals: string[] = []
    await exerciseTodoListReplacement(todoContext(), {
      steps: ['Inspect the repository', 'Report their purpose'],
      approveWrite: async (step) => {
        approvals.push(step)
        fake.events.push(`approve ${step}`)
      },
    })
    expect(approvals).toEqual(['Inspect the repository', 'Report their purpose'])
    expect(fake.events.slice(0, 5)).toEqual(['queue 2', 'send Write a 2-step to-do list.', 'steps 1', 'approve Inspect the repository', 'steps 2'])
  })

  it('runs the check of the caller after the first list', async () => {
    const afterFirstList = async () => {
      fake.events.push('first list row')
    }
    await exerciseTodoListReplacement(todoContext(), { afterFirstList })
    expect(fake.events.indexOf('first list row')).toBe(fake.events.indexOf('count [data-task-checkbox="pending"]=1') + 1)
  })

  it('fails when the sidebar keeps an item in progress after the replacement', async () => {
    fake.counts['[data-task-checkbox="in_progress"]'] = 1
    await expect(exerciseTodoListReplacement(todoContext())).rejects.toThrow('holds 1 items, not 0')
  })
})
