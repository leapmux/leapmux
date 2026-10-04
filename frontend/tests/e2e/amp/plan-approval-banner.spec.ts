import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { ampToolResultReader } from '../helpers/ampToolResult'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { waitForSettingsHydrated } from '../helpers/ui'
import { exerciseMissingNativePlanMode } from '../helpers/unsupportedPlanMode'

ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest('runs the actual no-plan native route without a plan review request', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context: ManagedNativeScenarioContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  context.readToolResult = ampToolResultReader(context)
  await expectNoNativeControl(context, { testId: 'plan-approve-btn', relatedControl: () => expectNoNativeControl(context, { testId: 'plan-reject-btn', relatedControl: () => exerciseMissingNativePlanMode(context, { reload: false }) }) })
  await page.reload()
  await waitForSettingsHydrated(page, 'permissionMode')
  await expect(page.locator('[data-testid="plan-approve-btn"]:visible')).toHaveCount(0)
  await expect(page.locator('[data-testid="plan-reject-btn"]:visible')).toHaveCount(0)
})
