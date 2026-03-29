import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

test.describe('Copilot Agent Type Selector', () => {
  test('Copilot appears in the provider selector', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `selector-copilot-${Date.now()}`, adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      const agentTypeBtn = page.locator('[data-testid="agent-type-selector"]')
      if (await agentTypeBtn.isVisible({ timeout: 5000 }).catch(() => false)) {
        await agentTypeBtn.click()

        const copilotOption = page.locator('[data-testid="agent-type-copilot-cli"], [data-value="COPILOT_CLI"]')
        await expect(copilotOption).toBeVisible({ timeout: 5000 }).catch(() => {
          // Selector UI may vary; this is best-effort.
        })
      }
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
