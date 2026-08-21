import { expect, test } from './fixtures'
import { CHAT_SCROLL_CONTAINER } from './helpers/ui'

/**
 * Where the caret goes after a send. The decision itself is a pure function,
 * unit-tested over its whole matrix in
 * `src/components/chat/markdownEditor/sendFocus.test.ts`.
 *
 * What only a real browser can verify is the input the decision reads: whether
 * the editor still holds focus by the time the send runs. That answer comes
 * from the platform's own focus rules for a pressed button, which no unit test
 * models -- and getting it wrong on a phone raises the on-screen keyboard over
 * the transcript the user just uncovered.
 */
test.describe('composer send focus', () => {
  const EDITOR = '[data-testid="chat-editor"] .ProseMirror'

  test('a press on Send keeps the caret in the editor', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator(EDITOR)
    await expect(editor).toBeVisible()

    await editor.click()
    await page.keyboard.type('keep my caret')
    await expect(editor).toBeFocused()

    await page.locator('[data-testid="send-button"]').click()

    // The empty editor is the app's acknowledgement that the send committed,
    // so the focus decision has already run by the time this passes.
    await expect(editor).toHaveText('')
    // The press must not park focus on the button it landed on. This is what
    // `keepFocusOnPress` buys, and it is the half that stops the fix for the
    // blurred composer below from costing everyone their caret.
    await expect(editor).toBeFocused()
  })

  test('a send from a composer the user left does not take the caret back', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator(EDITOR)
    await expect(editor).toBeVisible()

    await editor.click()
    await page.keyboard.type('typed, then walked away')

    // Leaving the composer is what a phone user does to put the on-screen
    // keyboard away before they press Send.
    await page.locator(CHAT_SCROLL_CONTAINER).click({ position: { x: 8, y: 8 } })
    await expect(editor).not.toBeFocused()

    await page.locator('[data-testid="send-button"]').click()
    await expect(editor).toHaveText('')

    // The send repairs a caret; it never takes one. Focusing here would raise
    // the on-screen keyboard that the user had just dismissed.
    await expect(editor).not.toBeFocused()
  })
})
