import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { currentNativeAgent, nativeModelContextText, nativeModelToolNames } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsIdle } from '../helpers/ui'
import { kiloTest } from '../kilo-fixtures'

/**
 * Kilo's default primary agent. Kilo renames OpenCode's `build` agent to `code`
 * and deletes `build` (`patchAgents` in Kilo's
 * `packages/opencode/src/kilocode/agent/index.ts`), so the catalog offers no
 * `build`. The Worker's fallback states the same name (`kilo.PrimaryAgentCode`).
 */
const KILO_DEFAULT_PRIMARY_AGENT = 'code'

kiloTest('completes a native read-only plan without a dedicated approval banner', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }
  const primaryAgent = async () => (await currentNativeAgent(context)).optionGroups.find(group => group.id === 'primaryAgent')?.currentValue
  await expect.poll(primaryAgent).toBe(KILO_DEFAULT_PRIMARY_AGENT)
  await expectNoNativeControl(context, {
    testId: 'plan-approve-btn',
    relatedProof: () => exerciseNativeReadOnlyPlan(context, {
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
  })
  expect(await primaryAgent()).toBe('plan')
  await page.reload()
  await expectSettingsChip(page, 'Plan')
  await expect(page.locator('[data-testid="plan-approve-btn"]:visible')).toHaveCount(0)
  await chooseSettingsOption(page, `primaryAgent-${KILO_DEFAULT_PRIMARY_AGENT}`)
  await waitForSettingsIdle(page)
  expect(await primaryAgent()).toBe(KILO_DEFAULT_PRIMARY_AGENT)
})
