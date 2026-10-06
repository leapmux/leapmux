import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { currentNativeAgent, nativeModelContextText, nativeModelToolNames } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsIdle } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('completes a native read-only plan without a dedicated approval banner', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  await expectNoNativeControl(context, {
    testId: 'plan-approve-btn',
    relatedProof: () => exerciseNativeReadOnlyPlan(context, {
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
  })
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'primaryAgent')?.currentValue).toBe('plan')
  await page.reload()
  await expectSettingsChip(page, 'Plan')
  await expect(page.locator('[data-testid="plan-approve-btn"]:visible')).toHaveCount(0)
  await chooseSettingsOption(page, 'primaryAgent-build')
  await waitForSettingsIdle(page)
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'primaryAgent')?.currentValue).toBe('build')
})
