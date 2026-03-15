import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { ASSISTANT_BUBBLE_SELECTOR, assistantBubbles, loginViaToken, waitForWorkspaceReady } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test.describe('Worker Restart Thinking Indicator', () => {
  test('should hide thinking indicator when worker goes offline during agent turn', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, adminOrgId, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Thinking Indicator Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
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
      const streamingText = assistantBubbles(page)
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
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, adminOrgId, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Agent Resume Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
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
      await page.waitForFunction((sel: string) => {
        const bubbles = document.querySelectorAll(sel)
        for (const b of bubbles) {
          if (b.textContent?.includes('4'))
            return true
        }
        return false
      }, ASSISTANT_BUBBLE_SELECTOR, { timeout: 60_000 })

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

      // Install a MutationObserver BEFORE sending the message so we can
      // detect even a brief flash of the thinking indicator.
      await page.evaluate(() => {
        (window as any).__thinkingIndicatorSeen = false
        const observer = new MutationObserver(() => {
          if (document.querySelector('[data-testid="thinking-indicator"]')) {
            (window as any).__thinkingIndicatorSeen = true
            observer.disconnect()
          }
        })
        observer.observe(document.body, { childList: true, subtree: true })
        // Also check immediately in case it's already visible.
        if (document.querySelector('[data-testid="thinking-indicator"]')) {
          (window as any).__thinkingIndicatorSeen = true
          observer.disconnect()
        }
      })

      // Send a new message — agent should restart and respond
      await editor.click()
      await page.keyboard.type('What is 3+3? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')

      // Wait for the assistant's response containing "6"
      await page.waitForFunction((sel: string) => {
        const bubbles = document.querySelectorAll(sel)
        for (const b of bubbles) {
          if (b.textContent?.includes('6'))
            return true
        }
        return false
      }, ASSISTANT_BUBBLE_SELECTOR, { timeout: 60_000 })

      // Verify that the thinking indicator was shown at some point during
      // the turn, even if only briefly before streaming began.
      const sawThinking = await page.evaluate(() => (window as any).__thinkingIndicatorSeen)
      expect(sawThinking).toBe(true)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
