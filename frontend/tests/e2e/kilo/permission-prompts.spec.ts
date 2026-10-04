import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { KILO_E2E_SKIP_REASON, kiloTest } from '../kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

async function allowShellPermission(page: Page): Promise<void> {
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('printf provider-steer-ready')
  await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
}

kiloTest('permission-prompts: places queued guidance in the next native model request', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
  void authenticatedKiloWorkspace
  await exerciseProviderSteer(page, modelScript, AgentProvider.KILO, { approveTool: allowShellPermission, resultDividers: 2 })
})

kiloTest('keeps actual file bytes unchanged until the native Allow decision', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }
  await exerciseNativePermissionWrite(context)
})
