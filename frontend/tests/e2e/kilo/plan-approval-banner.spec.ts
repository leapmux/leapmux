import { expect } from '@playwright/test'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { currentNativeAgent, nativeModelContextText, nativeModelToolNames, nativeOptionValue } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsIdle } from '../helpers/ui'
import { expectNoPlanReview } from '../helpers/unsupportedPlanMode'
import { kiloTest } from '../kilo-fixtures'

/**
 * Kilo's default primary agent. Kilo renames OpenCode's `build` agent to `code`
 * and deletes `build` (`patchAgents` in Kilo's
 * `packages/opencode/src/kilocode/agent/index.ts`), so the catalog offers no
 * `build`. The Worker's fallback states the same name (`kilo.PrimaryAgentCode`).
 */
const KILO_DEFAULT_PRIMARY_AGENT = 'code'

kiloTest('completes a native read-only plan without a dedicated approval banner', async ({ native }) => {
  const { page } = native
  const primaryAgent = async () => nativeOptionValue(await currentNativeAgent(native), 'primaryAgent')
  await expect.poll(primaryAgent).toBe(KILO_DEFAULT_PRIMARY_AGENT)
  await expectNoPlanReview(native, {
    relatedProof: () => exerciseNativeReadOnlyPlan(native, {
      preparePlan: async () => {
        await chooseSettingsOption(page, 'primaryAgent-plan')
        await waitForSettingsIdle(page)
      },
      nativeProof: (request) => {
        expect(nativeModelContextText(request)).toContain('# Native Plan Mode')
        const tools = nativeModelToolNames(request)
        expect(tools.length).toBeGreaterThan(0)
        expect(tools.some(tool => /(?:enter|exit)[_-]?plan/i.test(tool))).toBe(false)
      },
    }),
    afterReload: () => expectSettingsChip(page, 'Plan'),
  })
  expect(await primaryAgent()).toBe('plan')
  await chooseSettingsOption(page, `primaryAgent-${KILO_DEFAULT_PRIMARY_AGENT}`)
  await waitForSettingsIdle(page)
  expect(await primaryAgent()).toBe(KILO_DEFAULT_PRIMARY_AGENT)
})
