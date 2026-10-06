/**
 * The unit tests check the order and the targets of the plan review absence proof and of the plan option absence
 * proof. The plan-approval-banner and plan-mode browser specs of each provider check the actual agents.
 */
import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PLAN_REVIEW_BUTTON_TEST_IDS } from './ui'
import { expectNoPlanOption, expectNoPlanReview } from './unsupportedPlanMode'

/** The browser actions and checks that the fakes record, in order. */
const recorded = vi.hoisted(() => ({ events: [] as string[], counts: {} as Record<string, number>, modes: [] as string[][] }))

vi.mock('./nativeControlObservation', () => ({
  expectNoNativeControl: async (_context: unknown, options: { testId: string, additionalTestIds?: readonly string[], relatedProof: () => Promise<void> }) => {
    recorded.events.push(`observe ${[options.testId, ...(options.additionalTestIds ?? [])].join(' ')}`)
    await options.relatedProof()
    recorded.events.push('observation ended')
  },
}))

vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  waitForNativeSettingsHydrated: async () => {
    recorded.events.push('hydrated')
  },
  openSettingsMenu: async (_page: unknown, groupId: string) => {
    recorded.events.push(`menu ${groupId}`)
    return { getByTestId: (testId: string) => ({ testId }) }
  },
  closeComposerMenus: async () => {
    recorded.events.push('close menus')
  },
}))

vi.mock('./nativeConversation', () => ({
  sendNativeAnswer: async () => {
    recorded.events.push('answer')
  },
}))

vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  currentNativeAgent: async () => {
    recorded.events.push('agent')
    const options = recorded.modes.shift()
    return { optionGroups: options === undefined ? [] : [{ id: 'permissionMode', options: options.map(id => ({ id })) }] }
  },
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    // A fake locator records its count check. Every other value reaches the real `expect`.
    expect: (target: unknown, message?: string) => typeof target === 'object' && target !== null && 'testId' in target
      ? {
          toHaveCount: async (count: number) => {
            const { testId } = target as { testId: string }
            recorded.events.push(`count ${testId}`)
            // The fake reports the count that the test set, so a present button fails as Playwright fails, with the
            // message of the helper.
            expect(recorded.counts[testId] ?? 0, message).toBe(count)
          },
        }
      : actual.expect(target, message),
  }
})

function fakeContext(): ManagedNativeScenarioContext {
  const page = Object.assign({} as Page, {
    getByTestId: (testId: string) => ({ testId }),
    reload: async () => {
      recorded.events.push('reload')
      return null
    },
  })
  return { page, provider: 0, workspaceId: 'workspace', leapmuxServer: { hubUrl: '', adminToken: '', workerId: '' } } as unknown as ManagedNativeScenarioContext
}

beforeEach(() => {
  recorded.events = []
  recorded.counts = {}
  recorded.modes = []
})

describe('expectNoPlanReview', () => {
  it('observes both plan buttons during the proof, reloads, waits for the live catalog, and counts both buttons', async () => {
    await expectNoPlanReview(fakeContext(), { relatedProof: async () => {
      recorded.events.push('proof')
    } })
    expect(recorded.events).toEqual([
      'observe plan-approve-btn plan-reject-btn',
      'proof',
      'observation ended',
      'reload',
      'hydrated',
      'count plan-approve-btn',
      'count plan-reject-btn',
    ])
  })

  it('runs the wait of the caller after the reload in place of the default wait', async () => {
    await expectNoPlanReview(fakeContext(), {
      relatedProof: async () => {},
      afterReload: async () => {
        recorded.events.push('chip')
      },
    })
    expect(recorded.events).toEqual([
      'observe plan-approve-btn plan-reject-btn',
      'observation ended',
      'reload',
      'chip',
      'count plan-approve-btn',
      'count plan-reject-btn',
    ])
  })

  it.each(PLAN_REVIEW_BUTTON_TEST_IDS)('fails when the reloaded page holds %s', async (testId) => {
    recorded.counts[testId] = 1
    await expect(expectNoPlanReview(fakeContext(), { relatedProof: async () => {} })).rejects.toThrow(`the reloaded page holds no ${testId}`)
  })

  it('does not reload when the proof fails', async () => {
    await expect(expectNoPlanReview(fakeContext(), { relatedProof: async () => {
      throw new Error('the native operation failed')
    } })).rejects.toThrow('the native operation failed')
    expect(recorded.events).not.toContain('reload')
  })
})

describe('expectNoPlanOption', () => {
  it('runs a native turn, then checks the catalog and the menu before and after a reload', async () => {
    recorded.modes = [['default', 'acceptEdits'], ['default', 'acceptEdits']]
    await expectNoPlanOption(fakeContext())
    expect(recorded.events).toEqual([
      'answer',
      'hydrated',
      'agent',
      'menu permissionMode',
      'count permissionMode-plan',
      'close menus',
      'reload',
      'hydrated',
      'agent',
      'menu permissionMode',
      'count permissionMode-plan',
      'close menus',
    ])
  })

  // Each case gives both passes a catalog, so only the catalog check can fail it.
  it.each([
    { label: 'before the reload', modes: [['default', 'plan'], ['default']] },
    { label: 'after the reload', modes: [['default'], ['default', 'plan']] },
  ])('fails for a catalog that offers plan $label', async ({ modes }) => {
    recorded.modes = modes
    await expect(expectNoPlanOption(fakeContext())).rejects.toThrow('the native mode catalog offers no plan value')
  })

  it.each([
    { label: 'no mode group', modes: [] },
    { label: 'an empty mode group', modes: [[]] },
  ])('fails for $label', async ({ modes }) => {
    recorded.modes = modes
    await expect(expectNoPlanOption(fakeContext())).rejects.toThrow('native mode catalog is absent')
  })

  it('fails for a menu that offers plan, before the reload', async () => {
    recorded.modes = [['default'], ['default']]
    recorded.counts['permissionMode-plan'] = 1
    await expect(expectNoPlanOption(fakeContext())).rejects.toThrow('the mode menu offers no plan value')
    expect(recorded.events).not.toContain('reload')
  })
})
