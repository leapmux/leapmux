import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { ARITHMETIC_PROMPT, expectAssistantAnswer, expectSettingsChip, expectUserMessage, loginViaToken, messageBubbles, openSettingsMenu, openWorkspace, visibleOnly } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test.describe('Settings and /clear after Worker restart', () => {
  test('should handle settings changes and /clear after worker restart', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)

    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Worker Restart Settings Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Step 1: Send a message and wait for a response (agent starts)
      await editor.click()
      await page.keyboard.type(ARITHMETIC_PROMPT)
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      // Wait for the assistant's response containing "6912"
      await expectAssistantAnswer(page)

      // Step 2: Restart the Worker (stop + start). All persistent data
      // (workspaces, agents, messages) is stored on the Worker's SQLite DB,
      // so the conversation should survive the restart.
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)
      await restartWorker(separateHubWorker)

      // Wait for the E2EE channels to reconnect and messages to reload.
      // The original conversation should be visible (loaded from Worker DB).
      await expectUserMessage(page, '1234 + 5678')
      await expectAssistantAnswer(page)

      // Helper: wait for a notification bubble to contain the expected text.
      const waitForNotification = (text: string) =>
        expect(visibleOnly(page.getByText(text))).toBeVisible()

      // Helper: wait for the settings loading spinner to disappear.
      const waitForSettingsIdle = () =>
        expect(page.locator('[data-testid="settings-loading-spinner"]')).not.toBeVisible()

      // Step 3: Change permission mode (Default → Plan Mode)
      await openSettingsMenu(page, 'permissionMode')
      await page.locator('[data-testid="permissionMode-plan"]').click()

      await expectSettingsChip(page, 'Plan')
      await waitForNotification('Mode (Default \u2192 Plan Mode)')
      await waitForSettingsIdle()

      // Step 4: Change effort (Low → Medium, default overridden via LEAPMUX_CLAUDE_DEFAULT_EFFORT in e2e)
      // Must happen before switching to Haiku, which hides the effort section.
      await openSettingsMenu(page, 'effort')
      await page.locator('[data-testid="effort-medium"]').click()

      await waitForNotification('Effort (Low \u2192 Medium)')
      await waitForSettingsIdle()

      // Step 5: Change model (Sonnet → Haiku)
      await openSettingsMenu(page, 'model')
      await page.locator('[data-testid="model-haiku"]').click()

      await waitForNotification('Model (Sonnet \u2192 Haiku)')

      // Step 6: Send /clear
      await editor.click()
      await page.keyboard.type('/clear')
      await page.keyboard.press('Meta+Enter')

      await waitForNotification('Context cleared')

      // Verify no "Failed to deliver" messages appeared
      const failedMessages = messageBubbles(page).filter({ hasText: 'Failed to deliver' })
      await expect(failedMessages).toHaveCount(0)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
