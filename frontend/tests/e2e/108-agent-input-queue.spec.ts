import { Buffer } from 'node:buffer'
import { loginViaToken, openWorkspace, sendMessage } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, processTest as test } from './process-control-fixtures'

test.describe('agent input queue', () => {
  test('persists paused input across clients, a reload, and a Worker restart, then supports queue changes', async ({ page, browser, authenticatedWorkspace, separateHubWorker }) => {
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()
    await page.getByTestId('queue-pause-button').click()
    await expect(page.getByTestId('queue-pause-button')).toHaveText('Resume Queue')

    const secondContext = await browser.newContext({ baseURL: separateHubWorker.hubUrl })
    const secondPage = await secondContext.newPage()
    await loginViaToken(secondPage, separateHubWorker.adminToken)
    await openWorkspace(secondPage, authenticatedWorkspace.workspaceId)
    await expect(secondPage.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()

    try {
      await expect(secondPage.getByTestId('queue-pause-button')).toHaveText('Resume Queue')

      const queuedFirst = `${'x'.repeat(1200)} full text tail`
      const queuedFirstPreview = queuedFirst.slice(0, 80)
      await page.getByTestId('file-input').setInputFiles({
        name: 'queued-input.txt',
        mimeType: 'text/plain',
        buffer: Buffer.from('queued attachment'),
      })
      await sendMessage(page, queuedFirst)
      await sendMessage(page, 'queued second')
      for (const clientPage of [page, secondPage]) {
        await expect(clientPage.getByTestId('agent-input-queue')).toContainText(queuedFirstPreview)
        await expect(clientPage.getByTestId('agent-input-queue')).not.toContainText('full text tail')
        await expect(clientPage.getByTestId('agent-input-queue')).toContainText('queued-input.txt')
        await expect(clientPage.getByTestId('agent-input-queue')).toContainText('queued second')
        await expect(clientPage.getByTestId('agent-input-queue')).toHaveCSS('overflow-y', 'auto')
      }

      await restartWorker(separateHubWorker)
      await ensureWorkerOnline(separateHubWorker)
      await page.reload()
      await expect(page.getByTestId('agent-input-queue')).toContainText(queuedFirstPreview)
      await expect(page.getByTestId('agent-input-queue')).toContainText('queued second')

      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await editor.fill('normal draft')
      await page.getByTestId('file-input').setInputFiles({
        name: 'normal-draft.txt',
        mimeType: 'text/plain',
        buffer: Buffer.from('normal attachment'),
      })
      await expect(page.getByTestId('attachment-pill')).toContainText('normal-draft.txt')
      const first = page.getByTestId(/queued-input-/).filter({ hasText: queuedFirstPreview })
      await first.getByRole('button', { name: 'Edit', exact: true }).click()
      await expect(editor).toContainText('full text tail')
      await expect(page.getByTestId('attachment-pill')).toContainText('queued-input.txt')
      await editor.fill('edited first')
      await page.keyboard.press('Meta+Enter')
      for (const clientPage of [page, secondPage])
        await expect(clientPage.getByTestId('agent-input-queue')).toContainText('edited first')
      await expect(page.getByTestId('agent-input-queue')).toContainText('queued-input.txt')
      await expect(editor).toHaveText('normal draft')
      await expect(page.getByTestId('attachment-pill')).toContainText('normal-draft.txt')

      const edited = page.getByTestId(/queued-input-/).filter({ hasText: 'edited first' })
      await edited.getByRole('button', { name: 'Edit', exact: true }).click()
      await expect(editor).toHaveText('edited first')
      await editor.fill('unsaved queue edit')
      await expect.poll(() => page.evaluate(() => {
        for (let index = 0; index < localStorage.length; index++) {
          const value = localStorage.getItem(localStorage.key(index) ?? '')
          if (value?.includes('unsaved queue edit'))
            return true
        }
        return false
      })).toBe(true)
      await page.reload()
      await expect(editor).toHaveText('unsaved queue edit')
      await expect(page.getByTestId('attachment-pill')).toContainText('queued-input.txt')
      const resumedEdit = page.getByTestId(/queued-input-/).filter({ hasText: 'edited first' })
      await resumedEdit.getByRole('button', { name: 'Cancel Edit' }).click()
      await expect(editor).toHaveText('normal draft')

      await edited.getByRole('button', { name: 'Edit', exact: true }).click()
      const secondClientItem = secondPage.getByTestId(/queued-input-/).filter({ hasText: 'edited first' })
      await secondClientItem.getByRole('button', { name: 'Take Over' }).click()
      await expect(secondPage.locator('[data-testid="chat-editor"] .ProseMirror')).toHaveText('edited first')
      await expect(editor).toHaveText('normal draft')
      await secondClientItem.getByRole('button', { name: 'Cancel Edit' }).click()

      const second = page.getByTestId(/queued-input-/).filter({ hasText: 'queued second' })
      await second.getByRole('button', { name: 'Move Up' }).click()
      const previews = page.getByTestId('agent-input-queue').locator('div').filter({ hasText: /^(edited first|queued second)$/ })
      await expect(previews.first()).toHaveText('queued second')

      await page.getByTestId(/queued-input-/).first().getByRole('button', { name: 'Delete' }).click()
      await page.getByTestId(/queued-input-/).first().getByRole('button', { name: 'Delete' }).click()
      for (const clientPage of [page, secondPage])
        await expect(clientPage.getByTestId('agent-input-queue')).toHaveCount(0)
      await page.getByTestId('queue-pause-button').click()
      await expect(page.getByTestId('queue-pause-button')).toHaveText('Pause Queue')
      await expect(secondPage.getByTestId('queue-pause-button')).toHaveText('Pause Queue')
    }
    finally {
      await secondContext.close()
    }
  })
})
