import { expect, test } from './fixtures'

test.describe('Markdown Paste', () => {
  test('pasting markdown list text creates a bullet list', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Paste markdown list content via clipboard API
    await page.evaluate(() => {
      const data = new DataTransfer()
      data.setData('text/plain', '- foo\n- bar\n- baz')
      const event = new ClipboardEvent('paste', { clipboardData: data, bubbles: true, cancelable: true })
      document.querySelector('.ProseMirror')!.dispatchEvent(event)
    })

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
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create bold text
    const boldBtn = page.locator('[data-testid="toolbar-bold"]')
    await boldBtn.click()
    await page.keyboard.type('bold text', { delay: 100 })
    await boldBtn.click()

    // Verify bold text exists
    await expect(editor.locator('strong')).toHaveText('bold text')

    // Step 1: Verify that the copy serializer produces markdown with
    // inline formatting (our clipboardTextSerializer override).
    // Capture clipboard text by intercepting DataTransfer.setData
    // during a copy triggered via execCommand.
    await page.keyboard.press('Meta+a')
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

    await page.evaluate((text) => {
      const dt = new DataTransfer()
      dt.setData('text/plain', text)
      const event = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })
      document.querySelector('.ProseMirror')!.dispatchEvent(event)
    }, clipboardText)

    // The pasted markdown should produce bold text
    await expect(editor.locator('strong')).toHaveText('bold text')
  })
})

test.describe('Paste Into Code Context', () => {
  test('paste fenced code block into code_block strips delimiters', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await expect(editor.locator('pre')).toBeVisible()

    // Set clipboard to a fenced code block and paste
    await page.evaluate(() => {
      const dt = new DataTransfer()
      dt.setData('text/plain', '```python\nprint("hello")\n```')
      const event = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })
      document.querySelector('.ProseMirror')!.dispatchEvent(event)
    })
    await page.waitForTimeout(200)

    // Should have stripped the fence delimiters
    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('print("hello")')
    expect(codeText).not.toContain('```')
  })

  test('paste inline code into code_block strips backticks', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await expect(editor.locator('pre')).toBeVisible()

    // Paste inline code
    await page.evaluate(() => {
      const dt = new DataTransfer()
      dt.setData('text/plain', '`myVariable`')
      const event = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })
      document.querySelector('.ProseMirror')!.dispatchEvent(event)
    })
    await page.waitForTimeout(200)

    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('myVariable')
  })

  test('paste plain text into code_block is unchanged', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await expect(editor.locator('pre')).toBeVisible()

    // Paste plain text (no backticks)
    await page.evaluate(() => {
      const dt = new DataTransfer()
      dt.setData('text/plain', 'just plain text')
      const event = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })
      document.querySelector('.ProseMirror')!.dispatchEvent(event)
    })
    await page.waitForTimeout(200)

    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('just plain text')
  })
})
