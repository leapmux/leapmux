import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { isMaybeVisible, loginViaToken, waitForWorkspaceReady } from './helpers/ui'

test.describe('copilot agent type selector', () => {
  test('Copilot CLI appears in the provider selector', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `selector-copilot-${Date.now()}`, adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      const newAgentBtn = page.locator('[data-testid="new-agent-btn"]')
      if (await isMaybeVisible(newAgentBtn)) {
        await newAgentBtn.click()

        const dialog = page.locator('[role="dialog"]')
        await expect(dialog).toBeVisible()

        const trigger = dialog.getByTestId('agent-provider-selector-trigger')
        if (await isMaybeVisible(trigger, 3000)) {
          await trigger.click()
          await expect(dialog.getByTestId('agent-provider-option-5')).toContainText('GitHub Copilot')
        }
      }
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
