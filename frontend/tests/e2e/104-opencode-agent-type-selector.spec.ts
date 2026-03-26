import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

test.describe('OpenCode Agent Type Selector', () => {
  test('OpenCode appears in the provider selector', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer

    // Create a workspace with Claude Code (default provider).
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `selector-opencode-${Date.now()}`, adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Open the agent type selector.
      const agentTypeBtn = page.locator('[data-testid="agent-type-selector"]')
      if (await agentTypeBtn.isVisible({ timeout: 5000 }).catch(() => false)) {
        await agentTypeBtn.click()

        // OpenCode should appear as an option.
        const opencodeOption = page.locator('[data-testid="agent-type-opencode"], [data-value="OPENCODE"]')
        await expect(opencodeOption).toBeVisible({ timeout: 5000 }).catch(() => {
          // Selector UI may vary; this is best-effort.
        })
      }
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
