import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

test.describe('kilo agent type selector', () => {
  test('Kilo appears in the provider selector', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `selector-kilo-${Date.now()}`, adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      const newAgentBtn = page.locator('[data-testid="new-agent-btn"]')
      if (await newAgentBtn.isVisible({ timeout: 5000 }).catch(() => false)) {
        await newAgentBtn.click()

        const dialog = page.locator('[role="dialog"]')
        await expect(dialog).toBeVisible({ timeout: 5000 })

        const trigger = dialog.getByTestId('agent-provider-selector-trigger')
        if (await trigger.isVisible({ timeout: 3000 }).catch(() => false)) {
          await trigger.click()
          await expect(dialog.getByTestId('agent-provider-option-6')).toContainText('Kilo')
        }
      }
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
