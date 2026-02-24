import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, loginViaToken, loginViaUI, waitForWorkspaceReady } from './helpers'
import { expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test.describe('Message Delivery Error', () => {
  test('should show delivery error when worker is offline and retry on reconnect', async ({ separateHubWorker, page }) => {
    // Previous test file may have stopped the worker without restarting
    await restartWorker(separateHubWorker)

    const { hubUrl, adminToken, workerId, adminOrgId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Delivery Error Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for an assistant response bubble
      await editor.click()
      await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      await expect(
        page.locator('[data-testid="message-bubble"][data-role="assistant"]').first(),
      ).toBeVisible()

      // Stop the worker and wait for the hub to confirm it's offline.
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)

      // Send a message while worker is offline
      await editor.click()
      await page.keyboard.type('This message should fail')
      await page.keyboard.press('Meta+Enter')

      // Assert delivery error is visible
      const errorIndicator = page.locator('[data-testid="message-error"]')
      await expect(errorIndicator).toBeVisible()
      await expect(errorIndicator).toContainText('Failed to deliver')

      // Assert Retry and Delete buttons are visible
      await expect(page.locator('[data-testid="message-retry-button"]')).toBeVisible()
      await expect(page.locator('[data-testid="message-delete-button"]')).toBeVisible()

      // Restart worker
      await restartWorker(separateHubWorker)
      await page.waitForTimeout(3000)

      // Click Retry
      await page.locator('[data-testid="message-retry-button"]').click()

      // Assert error disappears
      await expect(errorIndicator).not.toBeVisible()

      // Wait for agent response (confirming message was delivered)
      await page.waitForFunction(() => {
        const bubbles = document.querySelectorAll('[data-testid="message-bubble"][data-role="assistant"]')
        return bubbles.length >= 2
      })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should persist delivery error across page refresh', async ({ separateHubWorker, page }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Persist Error Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for assistant response
      await editor.click()
      await page.keyboard.type('What is 1+1? Reply with just the number.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      await expect(
        page.locator('[data-testid="message-bubble"][data-role="assistant"]').first(),
      ).toBeVisible()

      // Stop the worker and wait for the hub to confirm it's offline
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)

      // Send a message while offline
      await editor.click()
      await page.keyboard.type('This should fail and persist')
      await page.keyboard.press('Meta+Enter')

      // Assert error is visible
      const errorIndicator = page.locator('[data-testid="message-error"]')
      await expect(errorIndicator).toBeVisible()

      // Reload the page
      await page.reload()

      // Login again
      await loginViaUI(page)

      // Navigate to the workspace (should be in sidebar)
      await page.getByText('Persist Error Test').click()

      // Wait for messages to load and assert error is still visible
      await expect(page.locator('[data-testid="message-error"]')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should delete failed message', async ({ separateHubWorker, page }) => {
    // Previous test may have stopped the worker without restarting
    await restartWorker(separateHubWorker)

    const { hubUrl, adminToken, workerId, adminOrgId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Delete Error Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for assistant response
      await editor.click()
      await page.keyboard.type('What is 5+5? Reply with just the number.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      await expect(
        page.locator('[data-testid="message-bubble"][data-role="assistant"]').first(),
      ).toBeVisible()

      // Stop the worker and wait for the hub to confirm it's offline
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)

      // Count user messages before sending the failing one
      const userMsgCountBefore = await page.locator('[data-testid="message-bubble"][data-role="user"]').count()

      // Send a message while offline
      await editor.click()
      await page.keyboard.type('Delete this message')
      await page.keyboard.press('Meta+Enter')

      // Assert error is visible
      const errorIndicator = page.locator('[data-testid="message-error"]')
      await expect(errorIndicator).toBeVisible()

      // Click Delete
      await page.locator('[data-testid="message-delete-button"]').click()

      // Assert the failed message is removed
      await expect(errorIndicator).not.toBeVisible()
      await expect(page.locator('[data-testid="message-bubble"][data-role="user"]')).toHaveCount(userMsgCountBefore)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
