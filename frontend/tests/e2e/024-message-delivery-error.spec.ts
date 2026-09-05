import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { appMenuTrigger, firstAssistantBubble, loginViaToken, openWorkspace, userBubbles } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test.describe('Failed agent input enqueue', () => {
  test('keeps the draft and creates no transcript row while the worker is offline', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Failed Enqueue Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      await editor.fill('What is 1234 + 5678? Reply with only the number.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await expect(firstAssistantBubble(page)).toBeVisible()

      const userCount = await userBubbles(page).count()
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)
      await editor.fill('Keep this draft')
      await page.keyboard.press('Meta+Enter')

      await expect(editor).toHaveText('Keep this draft')
      await expect(userBubbles(page)).toHaveCount(userCount)

      await restartWorker(separateHubWorker)
      await ensureWorkerOnline(separateHubWorker)
      await expect(editor).toBeVisible()
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await expect(userBubbles(page)).toHaveCount(userCount + 1)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('persists the retained draft across a reload', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Retained Draft Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)
      await editor.fill('Draft survives reload')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('Draft survives reload')

      await restartWorker(separateHubWorker)
      await ensureWorkerOnline(separateHubWorker)
      await page.reload()
      await appMenuTrigger(page).waitFor({ state: 'visible' })
      await page.getByText('Retained Draft Test').click()
      await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toHaveText('Draft survives reload')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
