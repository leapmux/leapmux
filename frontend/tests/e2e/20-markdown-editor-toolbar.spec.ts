import { expect, test } from './fixtures'
import { enterAndExitPlanMode } from './helpers/plan-mode'

test.describe('Markdown Editor Toolbar', () => {
  test('should show active state on bold button when text is bold', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Click bold button
    const boldBtn = page.locator('[data-testid="toolbar-bold"]')
    await boldBtn.click()

    // Bold button should have the active class
    await expect(boldBtn).toHaveClass(/IconButton_active/)

    // Type some text — it should be bold
    await editor.click()
    await page.keyboard.type('bold text')
    await expect(editor.locator('strong')).toHaveText('bold text')

    // Bold button should still be active
    await expect(boldBtn).toHaveClass(/IconButton_active/)

    // Toggle bold off
    await boldBtn.click()
    await expect(boldBtn).not.toHaveClass(/IconButton_active/)
  })

  test('should select heading level from dropdown', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Click in editor first
    await editor.click()
    await page.keyboard.type('Some text')

    // Open heading dropdown
    const headingBtn = page.locator('[data-testid="toolbar-heading"]')
    await headingBtn.click()

    // Menu should appear
    const menu = page.locator('[data-testid="heading-menu"]')
    await expect(menu).toBeVisible()

    // Select Heading 2
    await page.locator('[data-testid="heading-2"]').click()

    // An h2 element should be in the editor
    await expect(editor.locator('h2')).toBeVisible()

    // Heading button should now be active
    await expect(headingBtn).toHaveClass(/IconButton_active/)

    // Convert back to paragraph
    await headingBtn.click()
    await expect(menu).toBeVisible()
    await page.locator('[data-testid="heading-paragraph"]').click()

    // h2 should be gone
    await expect(editor.locator('h2')).not.toBeVisible()
    await expect(headingBtn).not.toHaveClass(/IconButton_active/)
  })

  test('should insert code block when clicking code block button', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    await editor.click()

    // Click code block button
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()

    // A pre element should appear in the editor
    await expect(editor.locator('pre')).toBeVisible()

    // Code block button should be active
    await expect(codeBlockBtn).toHaveClass(/IconButton_active/)
  })

  test('should grow editor beyond old 120px limit', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Default mode is Cmd+Enter-to-send, so Enter creates newlines
    await editor.click()

    // Type many lines to exceed the old 120px limit
    for (let i = 0; i < 15; i++) {
      await page.keyboard.type(`Line ${i + 1}`, { delay: 100 })
      await page.keyboard.press('Enter')
    }

    // The editor wrapper should have grown (capped at 75% of container)
    const wrapper = page.locator('[data-testid="chat-editor"]')
    const height = await wrapper.evaluate(el => el.getBoundingClientRect().height)
    expect(height).toBeGreaterThan(60)
  })

  test('should use --mono-font-family CSS variable for code elements', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    await editor.click()
    await page.keyboard.type('some code')

    // Select all text, then apply inline code via toolbar
    await page.keyboard.press('Meta+a')
    const codeBtn = page.locator('[data-testid="toolbar-code"]')
    await codeBtn.click()

    // Check that the code element's computed font-family includes the fallback
    const fontFamily = await editor.locator('code').evaluate(
      el => window.getComputedStyle(el).fontFamily,
    )
    // Should contain at least one of the expected monospace fonts
    expect(fontFamily).toMatch(/HackNerdFont|Menlo|Monaco|Courier New|monospace/)
  })
})

test.describe('Toggleable List Buttons', () => {
  test('bullet list button toggles on and off', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    const bulletBtn = page.locator('[data-testid="toolbar-bullet-list"]')

    // Click bullet list → should activate
    await bulletBtn.click()
    await expect(bulletBtn).toHaveClass(/IconButton_active/)
    await expect(editor.locator('ul')).toBeVisible()

    // Click again → should deactivate (convert back to paragraph)
    await bulletBtn.click()
    await expect(bulletBtn).not.toHaveClass(/IconButton_active/)
    await expect(editor.locator('ul')).not.toBeVisible()
  })

  test('switching from bullet to ordered list', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('list item', { delay: 100 })

    const bulletBtn = page.locator('[data-testid="toolbar-bullet-list"]')
    const orderedBtn = page.locator('[data-testid="toolbar-ordered-list"]')

    // Create bullet list
    await bulletBtn.click()
    await expect(editor.locator('ul')).toBeVisible()
    await expect(bulletBtn).toHaveClass(/IconButton_active/)

    // Switch to ordered list
    await orderedBtn.click()
    await expect(editor.locator('ol')).toBeVisible()
    await expect(editor.locator('ul')).not.toBeVisible()
    await expect(orderedBtn).toHaveClass(/IconButton_active/)
    await expect(bulletBtn).not.toHaveClass(/IconButton_active/)
  })

  test('task list button wraps existing text into task list', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('my text', { delay: 100 })

    const taskBtn = page.locator('[data-testid="toolbar-tasklist"]')

    // Click task list button — should wrap the existing text into a task list item
    await taskBtn.click()
    await expect(taskBtn).toHaveClass(/IconButton_active/)

    // The task list item should contain the text we typed
    const taskItem = editor.locator('li[data-checked]')
    await expect(taskItem).toBeVisible()
    await expect(taskItem).toContainText('my text')

    // There should be no separate paragraph outside the list
    // (the text should not remain as a standalone paragraph)
    const topLevelParagraphs = editor.locator(':scope > p')
    await expect(topLevelParagraphs).toHaveCount(0)
  })

  test('switching from bullet to task list', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('task item', { delay: 100 })

    const bulletBtn = page.locator('[data-testid="toolbar-bullet-list"]')
    const taskBtn = page.locator('[data-testid="toolbar-tasklist"]')

    // Create bullet list
    await bulletBtn.click()
    await expect(editor.locator('ul')).toBeVisible()

    // Switch to task list
    await taskBtn.click()
    await expect(editor.locator('li[data-checked]')).toBeVisible()
    await expect(taskBtn).toHaveClass(/IconButton_active/)
    await expect(bulletBtn).not.toHaveClass(/IconButton_active/)
  })
})

test.describe('Code Language Label', () => {
  test('clicking language label opens popover and selecting language updates it', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()

    // Language label should show "plaintext" by default
    const langLabel = editor.locator('.code-lang-label')
    await expect(langLabel).toHaveText('plaintext')

    // Click the language label
    await langLabel.click()

    // The language input should appear
    const langInput = page.locator('[data-testid="code-lang-input"]')
    await expect(langInput).toBeVisible()

    // Type and select a language
    await langInput.fill('python')
    await page.keyboard.press('Enter')

    // The label should now show "python"
    await expect(langLabel).toHaveText('python')
  })
})

test.describe('Send Feedback Button Labels', () => {
  test('ExitPlanMode banner shows Reject when editor is empty, Send Feedback when typing', async ({ page, authenticatedWorkspace }) => {
    // Enter plan mode, write a dummy plan, and exit
    const banner = await enterAndExitPlanMode(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    const rejectBtn = page.locator('[data-testid="plan-reject-btn"]')

    // With empty editor, button should say "Reject"
    await expect(rejectBtn).toHaveText('Reject')

    // Type feedback into the editor — button should switch to "Send Feedback"
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('Please reconsider this approach')
    await expect(rejectBtn).toHaveText('Send Feedback')
  })
})
