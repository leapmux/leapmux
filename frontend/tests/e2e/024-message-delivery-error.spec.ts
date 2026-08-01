import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { assistantBubbles, firstAssistantBubble, loginViaToken, loginViaUI, openWorkspace, userBubbles, visibleOnly } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test.describe('Message Delivery Error', () => {
  test('should show delivery error when worker is offline and retry on reconnect', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)

    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Delivery Error Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for an assistant response bubble
      await editor.click()
      await page.keyboard.type('What is 1234 + 5678? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      await expect(firstAssistantBubble(page)).toBeVisible()

      // Stop the worker and wait for the hub to confirm it's offline.
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)

      // Send a message while worker is offline
      await editor.click()
      await page.keyboard.type('This message should fail')
      await page.keyboard.press('Meta+Enter')

      // Assert delivery error is visible
      const errorIndicator = visibleOnly(page.getByTestId('message-error'))
      await expect(errorIndicator).toBeVisible()
      await expect(errorIndicator).toContainText('Failed to deliver')

      // Assert Retry and Delete buttons are visible
      const retry = visibleOnly(page.getByTestId('message-retry-button'))
      await expect(retry).toBeVisible()
      await expect(visibleOnly(page.getByTestId('message-delete-button'))).toBeVisible()

      // Restart the worker and wait for the hub to see it again, rather than
      // sleeping a flat 3s and hoping: the retry below is only meaningful once
      // delivery can actually succeed.
      await restartWorker(separateHubWorker)
      await ensureWorkerOnline(separateHubWorker)

      // Click Retry
      await retry.click()

      // Assert error disappears
      await expect(errorIndicator).not.toBeVisible()

      // Wait for agent response (confirming message was delivered)
      await expect.poll(() => assistantBubbles(page).count()).toBeGreaterThanOrEqual(2)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should persist delivery error across page refresh', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Persist Error Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for assistant response
      await editor.click()
      await page.keyboard.type('What is 1234 + 5678? Reply with just the number.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      await expect(firstAssistantBubble(page)).toBeVisible()

      // Stop the worker and wait for the hub to confirm it's offline
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)

      // Send a message while offline
      await editor.click()
      await page.keyboard.type('This should fail and persist')
      await page.keyboard.press('Meta+Enter')

      // Assert error is visible
      const errorIndicator = visibleOnly(page.getByTestId('message-error'))
      await expect(errorIndicator).toBeVisible()

      // Restart the worker so the workspace is accessible after reload.
      // The local delivery error is persisted in localStorage and will
      // survive the page refresh.
      await restartWorker(separateHubWorker)

      // Reload the page
      await page.reload()

      // Login again
      await loginViaUI(page)

      // Navigate to the workspace (should be in sidebar)
      await page.getByText('Persist Error Test').click()

      // Wait for messages to load and assert error is still visible
      await expect(visibleOnly(page.getByTestId('message-error'))).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should delete failed message', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)

    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Delete Error Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for assistant response
      await editor.click()
      await page.keyboard.type('What is 1234 + 5678? Reply with just the number.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      await expect(firstAssistantBubble(page)).toBeVisible()

      // Stop the worker and wait for the hub to confirm it's offline
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)

      // Count user messages before sending the failing one
      const userMsgCountBefore = await userBubbles(page).count()

      // Send a message while offline
      await editor.click()
      await page.keyboard.type('Delete this message')
      await page.keyboard.press('Meta+Enter')

      // Assert error is visible
      const errorIndicator = visibleOnly(page.getByTestId('message-error'))
      await expect(errorIndicator).toBeVisible()

      // Click Delete
      await visibleOnly(page.getByTestId('message-delete-button')).click()

      // Assert the failed message is removed
      await expect(errorIndicator).not.toBeVisible()
      await expect(userBubbles(page)).toHaveCount(userMsgCountBefore)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
