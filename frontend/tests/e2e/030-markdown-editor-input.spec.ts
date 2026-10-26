import { expect } from '@playwright/test'
import { test } from './fixtures'
import { focusComposer, queuePauseButton, visibleOnly } from './helpers/ui'

/**
 * The Tab/Shift+Tab plugin (paragraph → heading promotion, list-item indent,
 * blockquote nesting, code-block tab stops, modifier-key guards) is
 * exhaustively unit-tested against a real Milkdown editor mounted in jsdom in
 * `src/lib/editor/keyboardPlugins.test.ts`. The list-item shape and Markdown
 * round-trip live in those tests too.
 *
 * What only a real browser can verify is that list creation actually wires
 * through Milkdown's input rules and that the resulting DOM matches what the
 * chat send-pipeline expects to read. This single smoke exercises that
 * end-to-end path: the `- ` input rule → bullet list → Tab to indent → check
 * the rendered nested list.
 */
test.describe('Markdown editor input — smoke', () => {
  test('focuses the text beside action buttons in a narrow empty composer', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await page.setViewportSize({ width: 1200, height: 800 })
    await page.addStyleTag({ content: '[data-testid="agent-editor-panel"] { max-width: 260px; }' })
    const box = visibleOnly(page.getByTestId('composer-box'))
    await expect(box).not.toHaveAttribute('data-expanded')
    const editor = await focusComposer(page)
    await expect(editor).toBeFocused()
    await page.keyboard.type('A narrow draft')
    await expect(editor).toHaveText('A narrow draft')
    await expect(queuePauseButton(page)).toHaveText('Pause Queue')
    await expect(page.getByTestId('schedule-send-popover')).toBeHidden()
  })

  test('the bullet-list input rule + Tab produces a nested list in the live editor', async ({ page, authenticatedWorkspace }) => {
    const editor = await focusComposer(page)
    // `- ` is the bullet-list input rule; the formatting toolbar it replaced
    // was deleted with the composer rewrite.
    await page.keyboard.type('- ', { delay: 50 })
    await page.keyboard.type('item 1', { delay: 50 })
    await page.keyboard.press('Enter')
    await page.keyboard.type('item 2', { delay: 50 })

    await page.keyboard.press('Tab')
    await expect(editor.locator('ul > li > ul > li')).toHaveText('item 2')

    await page.keyboard.press('Shift+Tab')
    await expect(editor.locator('ul:first-child > li')).toHaveCount(2)
  })
})
