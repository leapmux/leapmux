import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativeOptionSequence, exerciseNativePlanWithEffort, expectSettingsOptionsOffered } from './nativeSettings'

/** The page, menu, and turn events of one scenario, in order, and the answers of its turns. */
const browser = vi.hoisted(() => ({ events: [] as string[], answers: [] as string[], offered: [] as string[] }))

vi.mock('./ui', () => ({
  waitForNativeSettingsHydrated: async () => {
    browser.events.push('hydrated')
  },
  chooseSettingsOption: async (_page: Page, testId: string) => {
    browser.events.push(`choose ${testId}`)
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

vi.mock('./nativeScenario', () => ({
  expectNativeOptionValue: async (_context: unknown, groupId: string, value: string) => {
    browser.events.push(`worker ${groupId}=${value}`)
  },
}))

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
  leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
  workspaceId: 'workspace',
} satisfies ManagedNativeScenarioContext

beforeEach(() => {
  browser.events = []
  browser.answers = []
  browser.offered = []
  reload.mockClear()
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
