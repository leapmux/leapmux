import { expect, test } from './fixtures'

/**
 * The Tab/Shift+Tab plugin (paragraph → heading promotion, list-item indent,
 * blockquote nesting, code-block tab stops, modifier-key guards) is
 * exhaustively unit-tested against a real Milkdown editor mounted in jsdom in
 * `src/lib/editor/keyboardPlugins.test.ts`. The list-item shape and Markdown
 * round-trip live in those tests too.
 *
 * What only a real browser can verify is that the toolbar-driven list creation
 * actually wires through Milkdown's commands and that the resulting DOM
 * matches what the chat send-pipeline expects to read. This single smoke
 * exercises that end-to-end path: toolbar click → bullet list → Tab to indent
 * → check the rendered nested list.
 */
test.describe('Markdown editor input — smoke', () => {
  test('toolbar bullet list + Tab produces a nested list in the live editor', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    await editor.click()
    await page.locator('[data-testid="toolbar-bullet-list"]').click()
    await page.keyboard.type('item 1', { delay: 50 })
    await page.keyboard.press('Enter')
    await page.keyboard.type('item 2', { delay: 50 })

    await page.keyboard.press('Tab')
    await expect(editor.locator('ul > li > ul > li')).toHaveText('item 2')

    await page.keyboard.press('Shift+Tab')
    await expect(editor.locator('ul:first-child > li')).toHaveCount(2)
  })
})
