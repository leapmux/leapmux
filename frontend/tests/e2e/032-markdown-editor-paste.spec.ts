import { expect, test } from './fixtures'
import { codeBlockText, pasteText } from './helpers/composer'
import { focusComposer } from './helpers/ui'

test.describe('Markdown Paste', () => {
  test('pasting markdown list text creates a bullet list', async ({ page, authenticatedWorkspace }) => {
    const editor = await focusComposer(page)

    // Paste markdown list content through a paste event
    await pasteText(page, '- foo\n- bar\n- baz')

    // A bullet list should be created with the items
    await expect(editor.locator('ul')).toBeVisible()
    const items = editor.locator('ul > li')
    await expect(items).toHaveCount(3)
    await expect(items.nth(0)).toContainText('foo')
    await expect(items.nth(1)).toContainText('bar')
    await expect(items.nth(2)).toContainText('baz')
  })
})

test.describe('Clipboard Copy/Paste', () => {
  test('copy and paste preserves markdown structure', async ({ page, authenticatedWorkspace }) => {
    const editor = await focusComposer(page)

    // Create bold text. `**...**` is the strong input rule; the formatting
    // toolbar it replaced was deleted with the composer rewrite.
    await page.keyboard.type('**bold text**', { delay: 100 })

    // Verify bold text exists
    await expect(editor.locator('strong')).toHaveText('bold text')

    // Step 1: Verify that the copy serializer produces markdown with
    // inline formatting (our clipboardTextSerializer override).
    // Capture clipboard text by intercepting DataTransfer.setData
    // during a copy triggered via execCommand.
    await page.keyboard.press('Meta+a')
    // Wait for the selection to be established before copying.
    await page.evaluate(() => new Promise(r => requestAnimationFrame(r)))
    const clipboardText = await page.evaluate(() => {
      let captured = ''
      const origSetData = DataTransfer.prototype.setData
      DataTransfer.prototype.setData = function (format: string, value: string) {
        if (format === 'text/plain')
          captured = value
        return origSetData.call(this, format, value)
      }
      document.execCommand('copy')
      DataTransfer.prototype.setData = origSetData
      return captured
    })
    // clipboardTextSerializer should produce markdown with bold markers
    expect(clipboardText).toContain('**')

    // Step 2: Verify that pasting markdown with inline formatting
    // restores the marks (our enhanced paste plugin). Clear the editor
    // first, then paste the captured markdown via synthetic paste event.
    await page.keyboard.press('Meta+a')
    await page.keyboard.press('Backspace')
    await expect(editor.locator('strong')).toHaveCount(0)

    await pasteText(page, clipboardText)

    // The pasted markdown should produce bold text
    await expect(editor.locator('strong')).toHaveText('bold text')
  })
})

test.describe('Paste Into Code Context', () => {
  test('paste fenced code block into code_block strips delimiters', async ({ page, authenticatedWorkspace }) => {
    const editor = await focusComposer(page)

    // Create a code block
    // The ``` input rule fires on the third backtick.
    await page.keyboard.type('```')
    await expect(editor.locator('pre')).toBeVisible()

    // Set clipboard to a fenced code block and paste
    await pasteText(page, '```python\nprint("hello")\n```')

    // Should have stripped the fence delimiters
    // The paste reaches the document after the event returns, so the read retries.
    await expect.poll(() => codeBlockText(editor)).toBe('print("hello")')
    expect(await codeBlockText(editor)).not.toContain('```')
  })

  test('paste inline code into code_block strips backticks', async ({ page, authenticatedWorkspace }) => {
    const editor = await focusComposer(page)

    // Create a code block
    // The ``` input rule fires on the third backtick.
    await page.keyboard.type('```')
    await expect(editor.locator('pre')).toBeVisible()

    // Paste inline code
    await pasteText(page, '`myVariable`')

    // The paste reaches the document after the event returns, so the read retries.
    await expect.poll(() => codeBlockText(editor)).toBe('myVariable')
  })

  test('paste plain text into code_block is unchanged', async ({ page, authenticatedWorkspace }) => {
    const editor = await focusComposer(page)

    // Create a code block
    // The ``` input rule fires on the third backtick.
    await page.keyboard.type('```')
    await expect(editor.locator('pre')).toBeVisible()

    // Paste plain text (no backticks)
    await pasteText(page, 'just plain text')

    // The paste reaches the document after the event returns, so the read retries.
    await expect.poll(() => codeBlockText(editor)).toBe('just plain text')
  })
})
