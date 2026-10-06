import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { exerciseProviderSteer } from '../helpers/providerSteer'

async function allowShellPermission(page: Page): Promise<void> {
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('printf provider-steer-ready')
  await page.getByTestId('control-actions').getByRole('button', { name: 'Allow', exact: true }).click()
}

copilotTest('places queued guidance in the next native model request', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
  void authenticatedCopilotWorkspace
  await exerciseProviderSteer(page, modelScript, AgentProvider.GITHUB_COPILOT, { approveTool: allowShellPermission })
})
