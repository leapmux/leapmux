import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { COPILOT_E2E_SKIP_REASON, copilotTest } from '../copilot-fixtures'
import { exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { exerciseProviderSteer } from '../helpers/providerSteer'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

async function allowShellPermission(page: Page): Promise<void> {
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('printf provider-steer-ready')
  await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
}

copilotTest('permission-prompts: places queued guidance in the next native model request', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
  void authenticatedCopilotWorkspace
  await exerciseProviderSteer(page, modelScript, AgentProvider.GITHUB_COPILOT, { approveTool: allowShellPermission })
})

copilotTest('keeps actual file bytes unchanged until the native Allow decision', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  await exerciseNativePermissionWrite(context)
})
