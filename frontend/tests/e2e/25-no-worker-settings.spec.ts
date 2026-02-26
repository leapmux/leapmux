import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, loginViaToken, waitForWorkspaceReady } from './helpers'
import { ensureWorkerOnline, expect, restartHub, stopHub, stopWorker, processTest as test } from './process-control-fixtures'

test.describe('Settings and /clear without Worker', () => {
  test('should handle /clear and settings changes without worker', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)

    const { hubUrl, adminToken, workerId, adminOrgId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'No Worker Settings Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Step 1: Send a message and wait for a response (agent starts)
      await editor.click()
      await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      // Wait for the assistant's response containing "4"
      await page.waitForFunction(() => {
        const bubbles = document.querySelectorAll('[data-testid="message-bubble"][data-role="assistant"]')
        for (const b of bubbles) {
          if (b.textContent?.includes('4'))
            return true
        }
        return false
      })

      // Remember the workspace URL
      const workspaceUrl = page.url()

      // Step 2: Stop Worker and Hub
      await stopWorker()
      await stopHub()

      // Step 3: Restart only Hub — Worker stays down.
      await restartHub(separateHubWorker)

      // Reload the page to establish fresh connections to the restarted Hub.
      await page.goto(workspaceUrl)
      await expect(editor).toBeVisible()

      // Verify the original conversation is visible (loaded from Hub DB)
      await page.waitForFunction(() => {
        const userBubbles = document.querySelectorAll('[data-testid="message-bubble"][data-role="user"]')
        const assistantBubbles = document.querySelectorAll('[data-testid="message-bubble"][data-role="assistant"]')
        let hasUserMsg = false
        let hasAssistantResp = false
        for (const b of userBubbles) {
          if (b.textContent?.includes('2+2'))
            hasUserMsg = true
        }
        for (const b of assistantBubbles) {
          if (b.textContent?.includes('4'))
            hasAssistantResp = true
        }
        return hasUserMsg && hasAssistantResp
      })

      // Helper: wait for a notification bubble to contain the expected text.
      const waitForNotification = (text: string) =>
        expect(page.getByText(text)).toBeVisible()

      // Helper: wait for the settings loading spinner to disappear.
      const waitForSettingsIdle = () =>
        expect(page.locator('[data-testid="settings-loading-spinner"]')).not.toBeVisible()

      // Helper: open the settings menu, retrying if caught mid-close animation.
      const trigger = page.locator('[data-testid="agent-settings-trigger"]')
      const menu = page.locator('[data-testid="agent-settings-menu"]')
      const openSettingsMenu = async () => {
        await expect(async () => {
          if (!await menu.isVisible()) {
            await trigger.click()
          }
          await expect(menu).toBeVisible()
        }).toPass({ timeout: 5000 })
      }

      // Step 4: Change permission mode (Default → Plan Mode)
      await openSettingsMenu()
      await page.locator('[data-testid="permission-mode-plan"]').click()

      await expect(trigger).toContainText('Plan')
      await waitForNotification('Mode (Default \u2192 Plan Mode)')
      await waitForSettingsIdle()

      // Step 5: Change model (Sonnet → Haiku)
      await openSettingsMenu()
      await page.locator('[data-testid="model-haiku"]').click()

      await waitForNotification('Model (Sonnet \u2192 Haiku)')
      await waitForSettingsIdle()

      // Step 6: Change effort (High → Medium)
      await openSettingsMenu()
      await page.locator('[data-testid="effort-medium"]').click()

      await waitForNotification('Effort (High \u2192 Medium)')

      // Step 7: Send /clear
      await editor.click()
      await page.keyboard.type('/clear')
      await page.keyboard.press('Meta+Enter')

      await waitForNotification('Context cleared')

      // Verify no "Failed to deliver" messages appeared
      const failedMessages = page.locator('[data-testid="message-bubble"]:has-text("Failed to deliver")')
      await expect(failedMessages).toHaveCount(0)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
