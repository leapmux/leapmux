import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { currentNativeAgent, nativeModelContextText, nativeModelToolNames, nativeOptionValue } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsIdle } from '../helpers/ui'
import { expectNoPlanReview } from '../helpers/unsupportedPlanMode'

/** How one OpenCode-family provider runs its plan agent. */
export interface OpenCodeFamilyPlan {
  /** The primary agent that a new session selects, to which the scenario returns at its end. */
  defaultPrimaryAgent: string
  /**
   * The reminder that the provider adds to the model context of its plan agent: `OPENCODE_PLAN_REMINDER` of
   * `./settingsScenario.ts`, or `KILO_PLAN_REMINDER` of `../kilo/scenarios.ts`.
   */
  planReminder: string
}

/**
 * Prove that the plan agent of an OpenCode-family provider completes a native read-only plan with no plan review.
 * The plan request holds the plan reminder of the provider and offers no plan-mode tool. The plan agent survives a
 * reload, and the session then returns to its default primary agent.
 */
export async function exerciseOpenCodeFamilyReadOnlyPlan(context: ManagedNativeScenarioContext, plan: OpenCodeFamilyPlan): Promise<void> {
  const { page } = context
  const primaryAgent = async () => nativeOptionValue(await currentNativeAgent(context), 'primaryAgent')
  await expect.poll(primaryAgent).toBe(plan.defaultPrimaryAgent)
  await expectNoPlanReview(context, {
    relatedProof: () => exerciseNativeReadOnlyPlan(context, {
      preparePlan: async () => {
        await chooseSettingsOption(page, 'primaryAgent-plan')
        await waitForSettingsIdle(page)
      },
      nativeProof: (request) => {
        expect(nativeModelContextText(request)).toContain(plan.planReminder)
        const tools = nativeModelToolNames(request)
        expect(tools.length).toBeGreaterThan(0)
        expect(tools.some(tool => /(?:enter|exit)[_-]?plan/i.test(tool))).toBe(false)
      },
    }),
    afterReload: () => expectSettingsChip(page, 'Plan'),
  })
  expect(await primaryAgent()).toBe('plan')
  await chooseSettingsOption(page, `primaryAgent-${plan.defaultPrimaryAgent}`)
  await waitForSettingsIdle(page)
  expect(await primaryAgent()).toBe(plan.defaultPrimaryAgent)
}
