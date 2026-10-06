import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { exerciseNativeReadOnlyPlan } from '../helpers/nativeReadOnlyPlan'
import { currentNativeAgent, nativeModelContextText } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectNoControlBanner, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('uses the native exit permission without a dedicated plan approval banner', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await expectNoNativeControl(context, {
    testId: 'plan-approve-btn',
    relatedControl: () => exerciseNativeReadOnlyPlan(context, {
      preparePlan: async () => {
        await chooseSettingsOption(page, 'permissionMode-plan')
        await waitForSettingsIdle(page)
      },
      nativeProof: async (request) => {
        expect(nativeModelContextText(request).toLowerCase()).toContain('plan mode')
        const banner = await waitForControlBanner(page)
        await expect(banner).toContainText('exit_plan_mode')
        await expect(page.locator('[data-testid="plan-approve-btn"]:visible')).toHaveCount(0)
        await page.getByTestId('control-deny-btn').filter({ visible: true }).first().click()
        await expectNoControlBanner(page)
      },
    }),
  })
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe('plan')
})
