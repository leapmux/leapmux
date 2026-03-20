import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { ASSISTANT_BUBBLE_SELECTOR, loginViaToken, waitForWorkspaceReady } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test.describe('Settings and /clear after Worker restart', () => {
  test('should handle settings changes and /clear after worker restart', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)

    const { hubUrl, adminToken, adminOrgId, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Worker Restart Settings Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
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
      await page.waitForFunction((sel: string) => {
        const bubbles = document.querySelectorAll(sel)
        for (const b of bubbles) {
          if (b.textContent?.includes('4'))
            return true
        }
        return false
      }, ASSISTANT_BUBBLE_SELECTOR)

      // Step 2: Restart the Worker (stop + start). All persistent data
      // (workspaces, agents, messages) is stored on the Worker's SQLite DB,
      // so the conversation should survive the restart.
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)
      await restartWorker(separateHubWorker)

      // Wait for the E2EE channels to reconnect and messages to reload.
      // The original conversation should be visible (loaded from Worker DB).
      await page.waitForFunction((sel: string) => {
        const userBubbles = document.querySelectorAll('[data-testid="message-bubble"][data-role="user"]')
        const assistantBubbles = document.querySelectorAll(sel)
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
      }, ASSISTANT_BUBBLE_SELECTOR)

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

      // Step 3: Change permission mode (Default → Plan Mode)
      await openSettingsMenu()
      await page.locator('[data-testid="permission-mode-plan"]').click()

      await expect(trigger).toContainText('Plan')
      await waitForNotification('Mode (Default \u2192 Plan Mode)')
      await waitForSettingsIdle()

      // Step 4: Change effort (Low → Medium, default overridden via LEAPMUX_CLAUDE_DEFAULT_EFFORT in e2e)
      // Must happen before switching to Haiku, which hides the effort section.
      await openSettingsMenu()
      await page.locator('[data-testid="effort-medium"]').click()

      await waitForNotification('Effort (Low \u2192 Medium)')
      await waitForSettingsIdle()

      // Step 5: Change model (Sonnet → Haiku)
      await openSettingsMenu()
      await page.locator('[data-testid="model-haiku"]').click()

      await waitForNotification('Model (Sonnet \u2192 Haiku)')

      // Step 6: Send /clear
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
