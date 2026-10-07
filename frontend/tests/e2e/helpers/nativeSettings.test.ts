import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseAutomaticEffort, exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption, exerciseNativeOptionSequence, exerciseNativePlanWithEffort, expectEffortHiddenForModel, expectSettingsOptionsOffered } from './nativeSettings'

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
  modelLevels: {} as Record<string, string[] | null>,
  modelSettlements: {} as Record<string, string>,
  autoLevel: undefined as string | undefined,
  workerStates: [] as Record<string, string>[],
  modelMenu: undefined as string[] | undefined,
  chipCount: undefined as number | undefined,
}))

vi.mock('./retryUntilPass', () => ({ retryUntilPass: async (attempt: () => unknown) => attempt() }))

vi.mock('./ui', () => ({
  waitForNativeSettingsHydrated: async () => {
    browser.events.push('hydrated')
  },
  chooseSettingsOption: async (_page: Page, testId: string) => {
    browser.events.push(`choose ${testId}`)
    // A test ID is `<group>-<value>`, and a value can hold a hyphen.
    const separator = testId.indexOf('-')
    const group = testId.slice(0, separator)
    const value = testId.slice(separator + 1)
    browser.applied[group] = group === 'effort' && value === 'auto' ? browser.autoLevel ?? value : value
    if (group === 'model' && browser.modelSettlements[value] !== undefined)
      browser.applied.effort = browser.modelSettlements[value]!
  },
  waitForSettingsIdle: async () => {
    browser.events.push('idle')
  },
  expectSettingsOptionChosen: async (_page: Page, testId: string) => {
    browser.events.push(`chosen ${testId}`)
  },
  offeredSettingsOptions: async () => [...browser.modelMenu ?? browser.modelLevels[browser.applied.model ?? ''] ?? browser.offered],
  openPlusMenu: async () => { browser.events.push('plus') },
  closeComposerMenus: async () => { browser.events.push('close') },
  settingsGroupTrigger: () => ({ count: async () => browser.modelLevels[browser.applied.model ?? ''] === null ? 0 : 1 }),
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
    nativeAgentById: async (_context: unknown, id: string) => {
      browser.workerStates.push({ ...browser.applied })
      return { id, status: AgentStatus.ACTIVE, agentSessionId: 'session' }
    },
    nativeOptionGroup: (_agent: unknown, groupId: string) => {
      const levels = browser.modelLevels[browser.applied.model ?? '']
      if (groupId === 'effort' && levels === null)
        return undefined
      return { id: groupId, mutable: browser.mutable, options: (levels ?? ['low', 'high']).map(id => ({ id })) }
    },
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
  page: {
    reload,
    locator: () => ({ count: async () => browser.chipCount ?? (browser.modelLevels[browser.applied.model ?? ''] === null ? 0 : 1) }),
  } as unknown as Page,
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
  browser.modelLevels = {}
  browser.modelSettlements = {}
  browser.autoLevel = undefined
  browser.workerStates = []
  browser.modelMenu = undefined
  browser.chipCount = undefined
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

describe('expectEffortHiddenForModel', () => {
  it('requires the Worker model and the absent effort group before the menu and chip checks', async () => {
    browser.modelLevels.plain = null
    await expectEffortHiddenForModel(context, { model: 'plain', effortGroupId: 'effort' })
    expect(browser.workerStates).toEqual([{ model: 'plain' }])
    expect(browser.events).toEqual(['choose model-plain', 'idle', 'chosen model-plain', 'plus', 'close'])
  })

  it('rejects a Worker catalog that still offers an effort', async () => {
    browser.modelLevels.plain = ['off']
    await expect(expectEffortHiddenForModel(context, { model: 'plain', effortGroupId: 'effort' })).rejects.toThrow('offers no effort level')
    expect(browser.events).not.toContain('plus')
  })

  it('rejects a remaining effort chip even when the native group and submenu are absent', async () => {
    browser.modelLevels.plain = null
    browser.chipCount = 1
    await expect(expectEffortHiddenForModel(context, { model: 'plain', effortGroupId: 'effort' })).rejects.toThrow('expected 1 to be +0')
  })
})

describe('exerciseEffortModelRoundTrip', () => {
  const options = {
    effortGroupId: 'effort',
    model: 'start',
    chosen: 'high',
    via: 'plain',
    viaEfforts: 'hidden',
    settled: 'off',
    nativeProof: vi.fn(),
  } as const

  it('compares the restored menu and proves the settled level in the next native request', async () => {
    browser.modelLevels = { start: ['high', 'low', 'off'], plain: null }
    browser.modelSettlements.plain = 'off'
    const proof = vi.fn()
    await exerciseEffortModelRoundTrip(context, { ...options, nativeProof: proof })
    expect(browser.workerStates).toContainEqual({ model: 'plain', effort: 'off' })
    expect(browser.applied).toEqual({ model: 'start', effort: 'off' })
    expect(browser.events).toContain('chosen effort-off')
    expect(proof).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ stepIndex: 0 }))
    expect(browser.answers).toHaveLength(1)
  })

  it('checks the exact ladder of an intermediate model whose effort control remains', async () => {
    browser.modelLevels = { start: ['high', 'low', 'off'], plain: ['low', 'off'] }
    browser.modelSettlements.plain = 'off'
    await exerciseEffortModelRoundTrip(context, { ...options, viaEfforts: ['off', 'low'] })
    expect(browser.events).not.toContain('plus')
    expect(browser.applied.effort).toBe('off')
  })

  it('rejects a menu that disagrees with the Worker before it starts the trip', async () => {
    browser.modelLevels.start = ['high', 'low']
    browser.modelMenu = ['high']
    await expect(exerciseEffortModelRoundTrip(context, options)).rejects.toThrow('effort menu of start')
    expect(browser.events).not.toContain('choose model-plain')
    expect(browser.answers).toEqual([])
  })

  it('rejects an intermediate ladder that differs from the required levels', async () => {
    browser.modelLevels = { start: ['high', 'off'], plain: ['high', 'off'] }
    await expect(exerciseEffortModelRoundTrip(context, { ...options, viaEfforts: ['off'] })).rejects.toThrow('levels of plain')
    expect(browser.answers).toEqual([])
  })

  it('rejects a trip whose second model is the first model', async () => {
    await expect(exerciseEffortModelRoundTrip(context, { ...options, via: 'start' })).rejects.toThrow('second model')
    expect(browser.events).toEqual([])
  })

  it('rejects a second model that offers the chosen level', async () => {
    await expect(exerciseEffortModelRoundTrip(context, { ...options, viaEfforts: ['high', 'off'] })).rejects.toThrow('lack the chosen level high')
    expect(browser.events).toEqual([])
  })
})

describe('exerciseAutomaticEffort', () => {
  it('requires the runtime level and proves the next request', async () => {
    browser.autoLevel = 'medium'
    const proof = vi.fn()
    await exerciseAutomaticEffort(context, { effortGroupId: 'effort', runs: 'medium', nativeProof: proof })
    expect(browser.events).toContain('chosen effort-medium')
    expect(browser.workerStates).toEqual([{ effort: 'medium' }])
    expect(proof).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ stepIndex: 0 }))
  })

  it('rejects a runtime that keeps Auto instead of the required level', async () => {
    await expect(exerciseAutomaticEffort(context, { effortGroupId: 'effort', runs: 'medium', nativeProof: () => {} })).rejects.toThrow('Worker applied effort=medium')
    expect(browser.answers).toEqual([])
  })

  it('rejects Auto as the level of the runtime proof', async () => {
    await expect(exerciseAutomaticEffort(context, { effortGroupId: 'effort', runs: 'auto', nativeProof: () => {} })).rejects.toThrow('level that the agent runs')
    expect(browser.events).toEqual([])
  })
})
