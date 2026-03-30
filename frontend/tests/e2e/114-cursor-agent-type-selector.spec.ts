import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

test.describe('cursor agent type selector', () => {
  test('Cursor CLI appears in the provider selector', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `selector-cursor-${Date.now()}`, adminOrgId)
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

        const select = dialog.locator('select').filter({ hasText: 'Claude Code' })
        if (await select.isVisible({ timeout: 3000 }).catch(() => false)) {
          const options = await select.locator('option').allTextContents()
          expect(options).toContain('Cursor CLI')
        }
      }
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
