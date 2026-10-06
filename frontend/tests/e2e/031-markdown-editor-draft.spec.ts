import { expect, test } from './fixtures'
import { composerEditor, focusComposer, waitForEditorDraft } from './helpers/ui'

/**
 * Per-agent draft isolation, the load/save/clear contract, and the empty-string
 * removal behavior are unit-tested in `src/lib/editor/draftPersistence.test.ts`.
 * What only a real browser can verify end-to-end is that the debounced save
 * fires, the stored draft survives a page reload, and Milkdown restores
 * the persisted markdown into the editor on remount.
 */
test.describe('Draft Persistence', () => {
  test('draft survives page reload', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace
    await focusComposer(page)
    await page.keyboard.type('draft text to preserve', { delay: 100 })

    // Wait until the debounced save reaches browser storage. A fixed sleep
    // guesses at the debounce and at the write queue behind it.
    await waitForEditorDraft(page, leapmuxServer.adminUserId, 'draft text to preserve')

    await page.reload()
    await expect(composerEditor(page)).toContainText('draft text to preserve')
  })
})
