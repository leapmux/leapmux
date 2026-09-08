import { expect, test } from './fixtures'

/**
 * Per-agent draft isolation, the load/save/clear contract, and the empty-string
 * removal behavior are unit-tested in `src/lib/editor/draftPersistence.test.ts`.
 * What only a real browser can verify end-to-end is that the debounced save
 * fires, the stored draft survives a page reload, and Milkdown restores
 * the persisted markdown into the editor on remount.
 */
test.describe('Draft Persistence', () => {
  test('draft survives page reload', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    await editor.click()
    await page.keyboard.type('draft text to preserve', { delay: 100 })

    // Wait for the 500ms debounced save plus a small margin.
    await page.waitForTimeout(700)

    await page.reload()
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toBeVisible()
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toContainText('draft text to preserve')
  })
})
