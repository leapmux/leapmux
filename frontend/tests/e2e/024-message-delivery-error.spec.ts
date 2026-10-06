import { getUserId } from './helpers/api'
import { withCleanup } from './helpers/cleanup'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { expectToastRecorded } from './helpers/toast'
import { appMenuTrigger, composerEditor, userBubbles, waitForEditorDraft, workspaceRowTitle } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

// Each test stops the worker-scoped Worker. The cleanup brings it back after a
// failure, so the delete of the test workspace and a later test of this
// Playwright worker do not fail for it.

test.describe('Failed agent input enqueue', () => {
  test('keeps the draft and creates no transcript row while the worker is offline', async ({ separateHubWorker, page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace
    await withCleanup(async () => {
      const editor = composerEditor(page)

      // One real turn first, so the counts below start from a live agent. The
      // SUBJECT is the refusal while the worker is offline, so the answer is
      // scripted and only has to arrive.
      await sendScriptedTurn(page, modelScript)

      const userCount = await userBubbles(page).count()
      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)
      // MARKED, because the restart below sends this very draft to the agent:
      // an unmarked prompt reaches the ambient scenario, which refuses it. The
      // assertion therefore reads `toContainText`, since the marker rides along.
      await editor.fill(modelScript.prompt('Keep this draft'))
      await page.keyboard.press('Meta+Enter')

      // Wait for the refusal. The unchanged editor alone can precede the request's result.
      await expectToastRecorded(page, 'worker is offline')
      await expect(editor).toContainText('Keep this draft')
      await expect(userBubbles(page)).toHaveCount(userCount)

      await restartWorker(separateHubWorker)
      await ensureWorkerOnline(separateHubWorker)
      await expect(editor).toBeVisible()
      await modelScript.queue({ text: 'Draft received.' })
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await expect(userBubbles(page)).toHaveCount(userCount + 1)
      await modelScript.waitForSteps()
    }, () => ensureWorkerOnline(separateHubWorker))
  })

  test('persists the retained draft across a reload', async ({ separateHubWorker, page, authenticatedWorkspace }) => {
    const { workspaceId } = authenticatedWorkspace
    await withCleanup(async () => {
      const editor = composerEditor(page)
      await expect(editor).toBeVisible()

      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)
      await editor.fill('Draft survives reload')
      await page.keyboard.press('Meta+Enter')
      await expectToastRecorded(page, 'worker is offline')
      await expect(editor).toHaveText('Draft survives reload')
      await waitForEditorDraft(page, await getUserId(separateHubWorker.hubUrl, separateHubWorker.adminToken), 'Draft survives reload')

      await restartWorker(separateHubWorker)
      await ensureWorkerOnline(separateHubWorker)
      await page.reload()
      await appMenuTrigger(page).waitFor({ state: 'visible' })
      await workspaceRowTitle(page, workspaceId).click()
      await expect(composerEditor(page)).toHaveText('Draft survives reload')
    }, () => ensureWorkerOnline(separateHubWorker))
  })
})
