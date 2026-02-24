import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, loginViaToken, waitForWorkspaceReady } from './helpers'
import { expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test.describe('Worker Restart Thinking Indicator', () => {
  // Infrastructure-dependent: timing between agent processing and worker stop
  // can vary under heavy load when 4 parallel workers share system resources.
  test.describe.configure({ retries: 1 })

  test('should hide thinking indicator when worker goes offline during agent turn', async ({ separateHubWorker, page }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Thinking Indicator Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message to start an agent turn
      await editor.click()
      await page.keyboard.type('Write a very long essay about the history of computing. Make it extremely detailed.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      // Wait for thinking indicator or streaming to appear (agent is processing)
      const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
      const streamingText = page.locator('[data-testid="message-bubble"][data-role="assistant"]')
      await expect(thinkingIndicator.or(streamingText)).toBeVisible({ timeout: 30_000 })

      // Stop the worker while agent is working
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)

      // Thinking indicator should disappear (agent status becomes INACTIVE)
      await expect(thinkingIndicator).not.toBeVisible({ timeout: 30_000 })

      // Interrupt button should also disappear
      const interruptButton = page.locator('[data-testid="interrupt-button"]')
      await expect(interruptButton).not.toBeVisible()
    }
    finally {
      await restartWorker(separateHubWorker).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should resume agent after worker restart and new message', async ({ separateHubWorker, page }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Agent Resume Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for a response
      await editor.click()
      await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')

      // Wait for the assistant's response
      await page.waitForFunction(() => {
        const bubbles = document.querySelectorAll('[data-testid="message-bubble"][data-role="assistant"]')
        for (const b of bubbles) {
          if (b.textContent?.includes('4'))
            return true
        }
        return false
      }, { timeout: 60_000 })

      // Stop the worker
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)

      // Thinking indicator should not be visible
      const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
      await expect(thinkingIndicator).not.toBeVisible()

      // Restart the worker
      await restartWorker(separateHubWorker)

      // Thinking indicator should still be hidden (agent not auto-restarted)
      await expect(thinkingIndicator).not.toBeVisible()

      // Send a new message — agent should restart and respond
      await editor.click()
      await page.keyboard.type('What is 3+3? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')

      // Wait for the assistant's response containing "6"
      await page.waitForFunction(() => {
        const bubbles = document.querySelectorAll('[data-testid="message-bubble"][data-role="assistant"]')
        for (const b of bubbles) {
          if (b.textContent?.includes('6'))
            return true
        }
        return false
      }, { timeout: 60_000 })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
