import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { waitForSettingsHydrated } from '../helpers/ui'
import { exerciseMissingNativePlanMode } from '../helpers/unsupportedPlanMode'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('runs the actual no-plan native route without a plan review request', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
  const context: ManagedNativeScenarioContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
  await expectNoNativeControl(context, { testId: 'plan-approve-btn', relatedControl: () => expectNoNativeControl(context, { testId: 'plan-reject-btn', relatedControl: () => exerciseMissingNativePlanMode(context, { reload: false }) }) })
  await page.reload()
  await waitForSettingsHydrated(page)
  await expect(page.locator('[data-testid="plan-approve-btn"]:visible')).toHaveCount(0)
  await expect(page.locator('[data-testid="plan-reject-btn"]:visible')).toHaveCount(0)
})
