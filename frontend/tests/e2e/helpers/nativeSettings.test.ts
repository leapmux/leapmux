import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption, exerciseNativeOptionSequence, exerciseNativePlanWithEffort, expectSettingsOptionsOffered } from './nativeSettings'

/**
 * The page, menu, and turn events of one scenario, in order, and the answers of its turns. `applied` holds the value
 * of each option group that a choice set, as the Worker reports it.
 */
const browser = vi.hoisted(() => ({
  events: [] as string[],
  answers: [] as string[],
  offered: [] as string[],
  applied: {} as Record<string, string>,
  mutable: true,
}))

vi.mock('./ui', () => ({
  waitForNativeSettingsHydrated: async () => {
    browser.events.push('hydrated')
  },
  chooseSettingsOption: async (_page: Page, testId: string) => {
    browser.events.push(`choose ${testId}`)
    // A test ID is `<group>-<value>`, and a value can hold a hyphen.
    const separator = testId.indexOf('-')
    browser.applied[testId.slice(0, separator)] = testId.slice(separator + 1)
  },
  waitForSettingsIdle: async () => {
    browser.events.push('idle')
  },
  expectSettingsOptionChosen: async (_page: Page, testId: string) => {
    browser.events.push(`chosen ${testId}`)
  },
  offeredSettingsOptions: async () => [...browser.offered],
}))

vi.mock('./nativeConversation', () => ({
  sendNativeAnswer: async (_context: unknown, prompt: string, answer: string) => {
    browser.events.push(`turn ${prompt}`)
    browser.answers.push(answer)
    return { stepIndex: browser.answers.length - 1, body: { answer } } as unknown as MockModelRequestRecord
  },
}))

vi.mock('./nativeScenario', async () => {
  const { AgentStatus } = await import('../../../src/generated/proto/leapmux/v1/agent_pb')
  return {
    expectNativeOptionValue: async (_context: unknown, groupId: string, value: string) => {
      browser.events.push(`worker ${groupId}=${value}`)
    },
    currentNativeAgent: async () => ({ id: 'agent' }),
    nativeAgentById: async (_context: unknown, id: string) => ({ id, status: AgentStatus.ACTIVE, agentSessionId: 'session' }),
    nativeOptionGroup: (_agent: unknown, groupId: string) => ({ id: groupId, mutable: browser.mutable, options: [{ id: 'low' }, { id: 'high' }] }),
    nativeOptionValue: (_agent: unknown, groupId: string) => browser.applied[groupId] ?? '',
  }
})

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return { ...actual, expect }
})

const reload = vi.fn(async () => {
  browser.events.push('reload')
})

const context = {
  page: { reload } as unknown as Page,
  modelScript: {} as never,
  provider: AgentProvider.CODEX,
  providerAgent: { provider: AgentProvider.CODEX, prefix: 'native-e2e' },
  leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
  workspaceId: 'workspace',
} satisfies ManagedNativeScenarioContext

beforeEach(() => {
  browser.events = []
  browser.answers = []
  browser.offered = []
  browser.applied = {}
  browser.mutable = true
  reload.mockClear()
})

/** A prepare step that records its place among the page events. */
async function recordedPrepare(): Promise<void> {
  browser.events.push('prepare')
}

/** The events of the restore step that both option helpers end with. */
const RESTORED_LOW_EFFORT_EVENTS = [
  'reload',
  'hydrated',
  'chosen effort-low',
  'worker effort=low',
  'turn Reply once after restoring the selected native setting.',
]

describe('exerciseNativeOption', () => {
  // Before the agent runs, the Worker offers a read-only model group in place of the live one. A prepare step that
  // opens a settings menu on that catalog reads the wrong options, and an open menu keeps its list until it closes.
  it('waits for the live catalog before the prepare step, and again after it', async () => {
    await exerciseNativeOption(context, { groupId: 'effort', value: 'low', prepare: recordedPrepare, nativeProof: () => {} })
    expect(browser.events).toEqual([
      'hydrated',
      'prepare',
      'hydrated',
      'choose effort-low',
      'idle',
      'chosen effort-low',
      'turn Reply once with the selected native setting.',
      ...RESTORED_LOW_EFFORT_EVENTS,
    ])
  })

  it('waits for the live catalog once without a prepare step', async () => {
    await exerciseNativeOption(context, { groupId: 'effort', value: 'low', nativeProof: () => {} })
    expect(browser.events).toEqual([
      'hydrated',
      'choose effort-low',
      'idle',
      'chosen effort-low',
      'turn Reply once with the selected native setting.',
      ...RESTORED_LOW_EFFORT_EVENTS,
    ])
  })

  it('stops before any choice when the prepare step fails', async () => {
    await expect(exerciseNativeOption(context, {
      groupId: 'effort',
      value: 'low',
      prepare: async () => {
        browser.events.push('prepare')
        throw new Error('the prepare step found no proxy model')
      },
      nativeProof: () => {},
    })).rejects.toThrow('no proxy model')
    expect(browser.events).toEqual(['hydrated', 'prepare'])
  })

  it('refuses a group that the live catalog does not let the user change, before any choice', async () => {
    browser.mutable = false
    await expect(exerciseNativeOption(context, { groupId: 'effort', value: 'low', nativeProof: () => {} })).rejects.toThrow('the live catalog lets the user change effort')
    expect(browser.events).toEqual(['hydrated'])
  })

  it('gives the proof the request of each turn', async () => {
    const proofs: unknown[] = []
    await exerciseNativeOption(context, { groupId: 'effort', value: 'low', nativeProof: (request) => {
      proofs.push(request.stepIndex)
    } })
    expect(proofs).toEqual([0, 1])
  })
})

describe('exerciseModelSwitchKeepsOption', () => {
  it('waits for the live catalog before the prepare step, and again after it', async () => {
    await exerciseModelSwitchKeepsOption(context, {
      prepare: recordedPrepare,
      kept: { groupId: 'effort', value: 'low' },
      model: 'alt',
      nativeProof: () => {},
    })
    expect(browser.events).toEqual([
      'hydrated',
      'prepare',
      'hydrated',
      'choose effort-low',
      'idle',
      'chosen effort-low',
      'choose model-alt',
      'idle',
      'chosen model-alt',
      'chosen effort-low',
      'turn Reply once after the model switch.',
      ...RESTORED_LOW_EFFORT_EVENTS,
    ])
  })

  it('waits for the live catalog once without a prepare step', async () => {
    await exerciseModelSwitchKeepsOption(context, { kept: { groupId: 'effort', value: 'low' }, model: 'alt', nativeProof: () => {} })
    expect(browser.events.slice(0, 2)).toEqual(['hydrated', 'choose effort-low'])
    expect(browser.events.filter(event => event === 'hydrated')).toHaveLength(2)
  })

  it('stops before any choice when the prepare step fails', async () => {
    await expect(exerciseModelSwitchKeepsOption(context, {
      prepare: async () => {
        browser.events.push('prepare')
        throw new Error('the prepare step chose no model')
      },
      kept: { groupId: 'effort', value: 'low' },
      model: 'alt',
      nativeProof: () => {},
    })).rejects.toThrow('chose no model')
    expect(browser.events).toEqual(['hydrated', 'prepare'])
  })
})

describe('exerciseNativeOptionSequence', () => {
  it('reaches each value through its own route, requires it as chosen, and proves its own turn', async () => {
    const proofs: string[] = []
    await exerciseNativeOptionSequence(context, {
      groupId: 'fastMode',
      steps: [
        { value: 'off', via: 'default' },
        { value: 'on', via: 'choose' },
        { value: 'on', via: 'reload' },
      ],
      nativeProof: (request, step, index) => {
        proofs.push(`${index} ${step.via} ${step.value} request ${request.stepIndex}`)
      },
    })
    expect(browser.events).toEqual([
      'hydrated',
      'chosen fastMode-off',
      'turn Reply once at fastMode step 0.',
      'choose fastMode-on',
      'idle',
      'chosen fastMode-on',
      'turn Reply once at fastMode step 1.',
      'reload',
      'hydrated',
      'chosen fastMode-on',
      'turn Reply once at fastMode step 2.',
    ])
    expect(proofs).toEqual(['0 default off request 0', '1 choose on request 1', '2 reload on request 2'])
  })

  it('gives each turn an answer that no other turn or sequence repeats', async () => {
    const steps = [{ value: 'on', via: 'choose' as const }, { value: 'on', via: 'reload' as const }]
    await exerciseNativeOptionSequence(context, { groupId: 'swarmMode', steps, nativeProof: () => {} })
    await exerciseNativeOptionSequence(context, { groupId: 'swarmMode', steps, nativeProof: () => {} })
    expect(new Set(browser.answers).size).toBe(4)
  })

  it('stops at the first failed proof', async () => {
    await expect(exerciseNativeOptionSequence(context, {
      groupId: 'fastMode',
      steps: [{ value: 'on', via: 'choose' }, { value: 'off', via: 'choose' }],
      nativeProof: () => {
        throw new Error('the native request states the wrong speed')
      },
    })).rejects.toThrow('the wrong speed')
    expect(browser.answers).toHaveLength(1)
  })

  it('refuses a sequence without a step', async () => {
    await expect(exerciseNativeOptionSequence(context, { groupId: 'fastMode', steps: [], nativeProof: () => {} })).rejects.toThrow('at least one step')
    expect(browser.events).toEqual([])
  })
})

describe('expectSettingsOptionsOffered', () => {
  it('accepts the stated values in any order', async () => {
    browser.offered = ['plan', 'build', 'edit', 'yolo']
    await expectSettingsOptionsOffered({} as Page, 'permissionMode', ['build', 'edit', 'plan', 'yolo'])
  })

  it('refuses a value that the menu adds', async () => {
    browser.offered = ['plan', 'build', 'auto']
    await expect(expectSettingsOptionsOffered({} as Page, 'permissionMode', ['plan', 'build'])).rejects.toThrow('permissionMode menu offers')
  })

  it('refuses a stated value that the menu lacks', async () => {
    browser.offered = ['plan']
    await expect(expectSettingsOptionsOffered({} as Page, 'permissionMode', ['plan', 'build'])).rejects.toThrow('permissionMode menu offers')
  })

  it('refuses an empty expectation', async () => {
    await expect(expectSettingsOptionsOffered({} as Page, 'permissionMode', [])).rejects.toThrow('at least one value')
  })
})

describe('exerciseNativePlanWithEffort', () => {
  it('answers the build turn and the Plan turn with different answers', async () => {
    const proofs: string[] = []
    await exerciseNativePlanWithEffort(context, {
      mode: { groupId: 'primaryAgent', value: 'plan' },
      effort: { groupId: 'effort', value: 'low' },
      restore: 'effort',
      nativeBuildProof: () => {
        proofs.push('build')
      },
      nativePlanProof: () => {
        proofs.push('plan')
      },
    })
    expect(proofs).toEqual(['build', 'plan', 'plan'])
    expect(new Set(browser.answers).size).toBe(browser.answers.length)
    expect(browser.events).toContain('worker effort=low')
  })
})
