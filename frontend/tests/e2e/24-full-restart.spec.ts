import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, loginViaToken, waitForWorkspaceReady } from './helpers'
import { expect, restartHub, restartWorker, stopHub, stopWorker, processTest as test } from './process-control-fixtures'

test.describe('Full Hub+Worker Restart', () => {
  test('should preserve chat history after hub and worker restart', async ({ separateHubWorker, page }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Full Restart Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Step 1: Send a message and wait for a response
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

      // Verify the user message is also visible
      const userBubbles = page.locator('[data-testid="message-bubble"][data-role="user"]')
      await expect(userBubbles.first()).toContainText('2+2')

      // Remember the workspace URL so we can navigate back after restart
      const workspaceUrl = page.url()

      // Step 2: Stop Worker first (so agent is terminated), then stop Hub
      await stopWorker()
      await stopHub()

      // Step 3: Start Hub and Worker back up
      await restartHub(separateHubWorker)
      await restartWorker(separateHubWorker)

      // Reload the page to establish fresh connections to the restarted Hub.
      await page.goto(workspaceUrl)

      // Wait for the editor to be ready after page reload
      await expect(editor).toBeVisible()

      // Verify the first conversation is still visible after restart (loaded from DB)
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

      // Step 4: Send another message and wait for response.
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
      })

      // Step 5: Verify both conversations are visible in chat history.
      await page.waitForFunction(() => {
        const userBubbles = document.querySelectorAll('[data-testid="message-bubble"][data-role="user"]')
        let has2plus2 = false
        let has3plus3 = false
        for (const b of userBubbles) {
          const text = b.textContent || ''
          if (text.includes('2+2'))
            has2plus2 = true
          if (text.includes('3+3'))
            has3plus3 = true
        }
        return has2plus2 && has3plus3
      })

      // Verify both assistant responses are present
      await page.waitForFunction(() => {
        const assistantBubbles = document.querySelectorAll('[data-testid="message-bubble"][data-role="assistant"]')
        let has4 = false
        let has6 = false
        for (const b of assistantBubbles) {
          const text = b.textContent || ''
          if (text.includes('4'))
            has4 = true
          if (text.includes('6'))
            has6 = true
        }
        return has4 && has6
      })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
