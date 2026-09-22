import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, getUserId, openAgentViaAPI } from './helpers/api'
import { getRecordedToasts } from './helpers/toast'
import { appMenuTrigger, ARITHMETIC_ANSWER_TEXT, expectAssistantAnswer, loginViaToken, openWorkspace, userBubbles, waitForAgentIdle, waitForEditorDraft } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test.describe('Failed agent input enqueue', () => {
  test('keeps the draft and creates no transcript row while the worker is offline', async ({ separateHubWorker, page, modelScript }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Failed Enqueue Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // One real turn first, so the counts below start from a live agent. The
      // SUBJECT is the refusal while the worker is offline, so the answer is
      // scripted and only has to arrive.
      await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
      await editor.fill(modelScript.prompt('What is 1234 + 5678? Reply with only the number.'))
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await modelScript.waitForSteps(1)
      await expectAssistantAnswer(page)
      await waitForAgentIdle(page)

      const userCount = await userBubbles(page).count()
      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)
      // MARKED, because the restart below sends this very draft to the agent:
      // an unmarked prompt reaches the ambient scenario, which refuses it. The
      // assertion therefore reads `toContainText`, since the marker rides along.
      await editor.fill(modelScript.prompt('Keep this draft'))
      await page.keyboard.press('Meta+Enter')

      // Wait for the refusal. The unchanged editor alone can precede the request's result.
      await expect.poll(async () => (await getRecordedToasts(page)).some(toast => toast.message.includes('worker is offline'))).toBe(true)
      await expect(editor).toContainText('Keep this draft')
      await expect(userBubbles(page)).toHaveCount(userCount)

      await restartWorker(separateHubWorker)
      await ensureWorkerOnline(separateHubWorker)
      await expect(editor).toBeVisible()
      await modelScript.queue({ text: 'Draft received.' })
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await expect(userBubbles(page)).toHaveCount(userCount + 1)
      await modelScript.waitForSteps(2)
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
      const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)
      await editor.fill('Draft survives reload')
      await page.keyboard.press('Meta+Enter')
      await expect.poll(async () => (await getRecordedToasts(page)).some(toast => toast.message.includes('worker is offline'))).toBe(true)
      await expect(editor).toHaveText('Draft survives reload')
      await waitForEditorDraft(page, await getUserId(hubUrl, adminToken), 'Draft survives reload')

      await restartWorker(separateHubWorker)
      await ensureWorkerOnline(separateHubWorker)
      await page.reload()
      await appMenuTrigger(page).waitFor({ state: 'visible' })
      await page.getByText('Retained Draft Test').click()
      await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toHaveText('Draft survives reload')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
