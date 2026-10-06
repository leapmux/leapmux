/**
 * The unit tests check the order and the targets of the plan review absence proof.
 * The plan-approval-banner browser specs of each provider check the actual transcript.
 */
import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { expectNoPlanReview, PLAN_REVIEW_BUTTON_TEST_IDS } from './unsupportedPlanMode'

/** The browser actions and checks that the fakes record, in order. */
const recorded = vi.hoisted(() => ({ events: [] as string[], counts: {} as Record<string, number> }))

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
}))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return {
    ...actual,
    expect: (target: { testId: string }) => ({
      toHaveCount: async (count: number) => {
        recorded.events.push(`count ${target.testId}`)
        // The fake reports the count that the test set, so a present button fails as Playwright fails.
        expect(recorded.counts[target.testId] ?? 0).toBe(count)
      },
    }),
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
    expect(recorded.events.slice(3)).toEqual(['reload', 'chip', 'count plan-approve-btn', 'count plan-reject-btn'])
  })

  it.each(PLAN_REVIEW_BUTTON_TEST_IDS)('fails when the reloaded page holds %s', async (testId) => {
    recorded.counts[testId] = 1
    await expect(expectNoPlanReview(fakeContext(), { relatedProof: async () => {} })).rejects.toThrow()
  })

  it('does not reload when the proof fails', async () => {
    await expect(expectNoPlanReview(fakeContext(), { relatedProof: async () => {
      throw new Error('the native operation failed')
    } })).rejects.toThrow('the native operation failed')
    expect(recorded.events).not.toContain('reload')
  })
})
