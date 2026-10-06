import { expect } from '@playwright/test'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { currentNativeAgent, nativeModelContextText, nativeModelToolNames, nativeOptionValue } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsIdle } from '../helpers/ui'
import { expectNoPlanReview } from '../helpers/unsupportedPlanMode'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('completes a native read-only plan without a dedicated approval banner', async ({ native }) => {
  const { page } = native
  const primaryAgent = async () => nativeOptionValue(await currentNativeAgent(native), 'primaryAgent')
  await expectNoPlanReview(native, {
    relatedProof: () => exerciseNativeReadOnlyPlan(native, {
      preparePlan: async () => {
        await chooseSettingsOption(page, 'primaryAgent-plan')
        await waitForSettingsIdle(page)
      },
      nativeProof: (request) => {
        expect(nativeModelContextText(request)).toContain('# Plan Mode - System Reminder')
        const tools = nativeModelToolNames(request)
        expect(tools.length).toBeGreaterThan(0)
        expect(tools.some(tool => /(?:enter|exit)[_-]?plan/i.test(tool))).toBe(false)
      },
    }),
    afterReload: () => expectSettingsChip(page, 'Plan'),
  })
  expect(await primaryAgent()).toBe('plan')
  await chooseSettingsOption(page, 'primaryAgent-build')
  await waitForSettingsIdle(page)
  expect(await primaryAgent()).toBe('build')
})
