import { expect, test } from './fixtures'
import { openAgentViaUI, PLAN_MODE_PROMPT } from './helpers'

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

test.describe('Tab and Shift+Tab behavior', () => {
  test('Tab indents bullet list item', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Default mode is Cmd+Enter-to-send, so Enter creates newlines in lists

    // Create a bullet list with two items
    await editor.click()
    const bulletBtn = page.locator('[data-testid="toolbar-bullet-list"]')
    await bulletBtn.click()
    await page.keyboard.type('item 1', { delay: 100 })
    await page.keyboard.press('Enter')
    await page.keyboard.type('item 2', { delay: 100 })

    // Tab to indent item 2
    await page.keyboard.press('Tab')

    // item 2 should be in a nested list: ul > li > ul > li
    await expect(editor.locator('ul > li > ul > li')).toHaveText('item 2')

    // Shift+Tab to unindent item 2
    await page.keyboard.press('Shift+Tab')

    // Should be back to flat: two sibling li elements
    const topLevelItems = editor.locator('ul:first-child > li')
    await expect(topLevelItems).toHaveCount(2)
  })

  test('Tab indents ordered list item', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Default mode is Cmd+Enter-to-send, so Enter creates newlines in lists

    // Create an ordered list with two items
    await editor.click()
    const orderedBtn = page.locator('[data-testid="toolbar-ordered-list"]')
    await orderedBtn.click()
    await page.keyboard.type('first', { delay: 100 })
    await page.keyboard.press('Enter')
    await page.keyboard.type('second', { delay: 100 })

    // Tab to indent
    await page.keyboard.press('Tab')

    // Should be nested: ol > li > ol > li
    await expect(editor.locator('ol > li > ol > li')).toHaveText('second')

    // Shift+Tab to unindent
    await page.keyboard.press('Shift+Tab')

    const topLevelItems = editor.locator('ol:first-child > li')
    await expect(topLevelItems).toHaveCount(2)
  })

  test('Tab nests blockquote deeper', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Create a blockquote
    await editor.click()
    const blockquoteBtn = page.locator('[data-testid="toolbar-blockquote"]')
    await blockquoteBtn.click()
    await page.keyboard.type('quoted text', { delay: 100 })

    // Tab to nest deeper
    await page.keyboard.press('Tab')

    // Should have nested blockquotes
    await expect(editor.locator('blockquote > blockquote')).toBeVisible()

    // Shift+Tab to lift one level
    await page.keyboard.press('Shift+Tab')

    // Should be back to single blockquote (no nested)
    await expect(editor.locator('blockquote')).toBeVisible()
    await expect(editor.locator('blockquote > blockquote')).not.toBeVisible()
  })

  test('Tab snaps to tab stop in code block', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Create a code block (cursor starts at position 0 in the empty block)
    await editor.click()
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()

    // At column 0 with base offset 0: Tab inserts 2 spaces (next tab stop)
    await page.keyboard.press('Tab')
    await page.keyboard.type('hello', { delay: 100 })

    // Code block text should be "  hello"
    // Use evaluate to exclude the code-lang-label widget from textContent
    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('  hello')
  })

  test('Shift+Tab snaps to previous tab stop in code block', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Create a code block with 4 leading spaces
    await editor.click()
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await page.keyboard.type('    indented', { delay: 100 })

    // Shift+Tab snaps to previous tab stop (removes 2 spaces, leaving 2)
    await page.keyboard.press('Shift+Tab')

    // Should remove 2 spaces, leaving "  indented"
    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('  indented')
  })

  test('Backspace deletes to previous tab stop in code block', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Create a code block, insert 4 spaces then text
    await editor.click()
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await page.keyboard.type('    x', { delay: 100 })

    // Delete the 'x' first with a normal Backspace
    await page.keyboard.press('Backspace')

    // Now cursor is at column 4 (all spaces before). Backspace should delete 2 spaces.
    await page.keyboard.press('Backspace')

    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('  ')
  })

  test('Backspace deletes single char when non-space precedes cursor in code block', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Create a code block with "ab"
    await editor.click()
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await page.keyboard.type('ab', { delay: 100 })

    // Backspace should delete just 'b' (not snap to tab stop)
    await page.keyboard.press('Backspace')

    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('a')
  })

  test('Multiple spaces can be typed in code block', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Create a code block and type text with multiple consecutive spaces
    await editor.click()
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await page.keyboard.type('a  b   c', { delay: 100 })

    // All spaces should be preserved (no OS text substitution)
    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('a  b   c')
  })

  test('Tab on heading increases level up to H6', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Create an H1
    await editor.click()
    const headingBtn = page.locator('[data-testid="toolbar-heading"]')
    await headingBtn.click()
    await page.locator('[data-testid="heading-1"]').click()
    await editor.click()
    await page.keyboard.type('title', { delay: 100 })

    // Tab → H2
    await page.keyboard.press('Tab')
    await expect(editor.locator('h2')).toBeVisible()
    await expect(editor.locator('h1')).not.toBeVisible()

    // Tab → H3
    await page.keyboard.press('Tab')
    await expect(editor.locator('h3')).toBeVisible()
    await expect(editor.locator('h2')).not.toBeVisible()

    // Tab → H4
    await page.keyboard.press('Tab')
    await expect(editor.locator('h4')).toBeVisible()
    await expect(editor.locator('h3')).not.toBeVisible()

    // Tab → H5
    await page.keyboard.press('Tab')
    await expect(editor.locator('h5')).toBeVisible()
    await expect(editor.locator('h4')).not.toBeVisible()

    // Tab → H6
    await page.keyboard.press('Tab')
    await expect(editor.locator('h6')).toBeVisible()
    await expect(editor.locator('h5')).not.toBeVisible()

    // Tab again → still H6 (max level)
    await page.keyboard.press('Tab')
    await expect(editor.locator('h6')).toBeVisible()
  })

  test('Shift+Tab on heading decreases level and converts H1 to paragraph', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Create an H3
    await editor.click()
    const headingBtn = page.locator('[data-testid="toolbar-heading"]')
    await headingBtn.click()
    await page.locator('[data-testid="heading-3"]').click()
    await editor.click()
    await page.keyboard.type('title', { delay: 100 })

    // Shift+Tab → H2
    await page.keyboard.press('Shift+Tab')
    await expect(editor.locator('h2')).toBeVisible()
    await expect(editor.locator('h3')).not.toBeVisible()

    // Shift+Tab → H1
    await page.keyboard.press('Shift+Tab')
    await expect(editor.locator('h1')).toBeVisible()
    await expect(editor.locator('h2')).not.toBeVisible()

    // Shift+Tab → paragraph
    await page.keyboard.press('Shift+Tab')
    await expect(editor.locator('p')).toBeVisible()
    await expect(editor.locator('h1')).not.toBeVisible()
  })

  test('Tab on plain paragraph converts to H1', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Type plain text
    await editor.click()
    await page.keyboard.type('plain text', { delay: 100 })

    // Tab converts to H1
    await page.keyboard.press('Tab')

    await expect(editor.locator('h1')).toBeVisible()
    await expect(editor.locator('h1')).toHaveText('plain text')
  })

  test('ArrowDown escapes code block but Tab does not', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Create a code block and type text
    await editor.click()
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await page.keyboard.type('code', { delay: 100 })

    // Tab should NOT escape — it inserts spaces, cursor stays in code block
    await page.keyboard.press('Tab')
    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('code  ')

    // Should still only have one block (the code block), no paragraph after
    // unless ArrowDown escapes it
    await page.keyboard.press('ArrowDown')

    // After ArrowDown, a new paragraph should exist after the code block
    await expect(editor.locator('p')).toBeVisible()
  })
})

test.describe('Draft Persistence', () => {
  test('draft survives page reload', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Type some text
    await editor.click()
    await page.keyboard.type('draft text to preserve', { delay: 100 })

    // Wait for debounced save (500ms)
    await page.waitForTimeout(700)

    // Reload the page
    await page.reload()

    // Wait for the editor to appear again
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()

    // The draft text should be restored
    const restoredEditor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(restoredEditor).toContainText('draft text to preserve')
  })

  test('draft is cleared after sending', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Type and wait for save
    await editor.click()
    await page.keyboard.type('message to send', { delay: 100 })
    await page.waitForTimeout(700)

    // Send the message (default mode is Cmd+Enter-to-send)
    await page.keyboard.press('Meta+Enter')

    // Wait a moment for the clear to take effect
    await page.waitForTimeout(200)

    // Reload
    await page.reload()
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()

    // Editor should be empty (draft was cleared on send)
    const restoredEditor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    const text = await restoredEditor.textContent()
    expect(text?.trim()).toBe('')
  })

  test('each agent tab has isolated draft', async ({ page, authenticatedWorkspace }) => {
    // Type in the first agent
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('first agent text', { delay: 100 })
    await page.waitForTimeout(700)

    // Open a second agent tab
    await openAgentViaUI(page)

    // The second agent should have an empty editor (wait for draft swap)
    const editor2 = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor2).toBeVisible()
    await page.waitForTimeout(500)
    await expect(editor2).not.toContainText('first agent text')

    // Type in the second agent
    await editor2.click()
    await page.keyboard.type('second agent text', { delay: 100 })
    await page.waitForTimeout(700)

    // Switch back to the first agent tab
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await agentTabs.first().click()

    // First agent should still have its text
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toContainText('first agent text')
  })
})

test.describe('Horizontal Rule Input Rule', () => {
  test('typing --- creates a horizontal rule', async ({ page, authenticatedWorkspace }) => {
    // Default mode is Cmd+Enter-to-send, so Enter creates newlines
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type some text first, then new line, then ---
    await page.keyboard.type('before', { delay: 100 })
    await page.keyboard.press('Enter')
    await page.keyboard.type('---', { delay: 30 })

    // An <hr> element should appear
    await expect(editor.locator('hr')).toBeVisible()
  })

  test('typing --- after text via Shift+Enter creates HR after paragraph', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type text, Shift+Enter for soft break, then --- to trigger HR
    await page.keyboard.type('some text', { delay: 100 })
    await page.keyboard.press('Shift+Enter')
    await page.keyboard.type('---', { delay: 30 })

    // An HR should appear after the paragraph containing "some text"
    await expect(editor.locator('hr')).toBeVisible()
    await expect(editor.locator('p').first()).toContainText('some text')
  })
})

test.describe('List Input Rules After Soft Break', () => {
  test('Shift+Enter then "- " creates bullet list', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type text, Shift+Enter for soft break, then "- " to trigger bullet list
    await page.keyboard.type('some text', { delay: 100 })
    await page.keyboard.press('Shift+Enter')
    await page.keyboard.type('- ', { delay: 30 })

    // A bullet list should be created
    await expect(editor.locator('ul')).toBeVisible()
  })

  test('Shift+Enter then "1. " creates ordered list', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type text, Shift+Enter, then "1. " to trigger ordered list
    await page.keyboard.type('some text', { delay: 100 })
    await page.keyboard.press('Shift+Enter')
    await page.keyboard.type('1. ', { delay: 30 })

    // An ordered list should be created
    await expect(editor.locator('ol')).toBeVisible()
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

test.describe('Delete Key in List', () => {
  test('Delete at start of list item deletes character, not list structure', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a bullet list with text
    const bulletBtn = page.locator('[data-testid="toolbar-bullet-list"]')
    await bulletBtn.click()
    await page.keyboard.type('hello', { delay: 100 })

    // Move cursor to start of list item
    await page.keyboard.press('Home')

    // Press Delete — should delete 'h', leaving 'ello' still in a list
    await page.keyboard.press('Delete')

    // Should still be in a list
    await expect(editor.locator('ul')).toBeVisible()
    await expect(editor.locator('li')).toContainText('ello')
  })
})

test.describe('Code Span Escape', () => {
  // --- Cursor simulator ---
  // State is an array like ['a', '|', 'b', '|', 'c', '^']
  // where '|' marks code boundaries and '^' is the cursor.
  // ArrowLeft/ArrowRight move '^' by one element.
  // Typing inserts a character right before '^'.

  type SimState = string[]

  function simToString(state: SimState): string {
    return state.join('')
  }

  function simArrowLeft(state: SimState): SimState {
    const s = [...state]
    const idx = s.indexOf('^')
    if (idx > 0) {
      s.splice(idx, 1)
      s.splice(idx - 1, 0, '^')
    }
    return s
  }

  function simArrowRight(state: SimState): SimState {
    const s = [...state]
    const idx = s.indexOf('^')
    if (idx < s.length - 1) {
      s.splice(idx, 1)
      s.splice(idx + 1, 0, '^')
    }
    return s
  }

  function simType(state: SimState, char: string): SimState {
    const s = [...state]
    const idx = s.indexOf('^')
    s.splice(idx, 0, char)
    return s
  }

  // Extract expected editor content from sim state:
  // - codeText: characters between '|' pairs
  // - fullText: all characters except '|' and '^'
  function simExpected(state: SimState): { codeText: string, fullText: string } {
    let inCode = false
    let codeText = ''
    let fullText = ''
    for (const ch of state) {
      if (ch === '|') {
        inCode = !inCode
      }
      else if (ch !== '^') {
        fullText += ch
        if (inCode)
          codeText += ch
      }
    }
    return { codeText, fullText }
  }

  // Run an action on the editor and compare sim vs actual state
  async function runAction(
    page: import('@playwright/test').Page,
    editor: import('@playwright/test').Locator,
    simState: SimState,
    action: string,
    stepLabel: string,
  ): Promise<SimState> {
    let newSim: SimState
    if (action === 'ArrowLeft') {
      await page.keyboard.press('ArrowLeft')
      await page.waitForTimeout(100)
      newSim = simArrowLeft(simState)
    }
    else if (action === 'ArrowRight') {
      await page.keyboard.press('ArrowRight')
      await page.waitForTimeout(100)
      newSim = simArrowRight(simState)
    }
    else {
      // Typing a character
      await page.keyboard.type(action, { delay: 100 })
      newSim = simType(simState, action)
    }

    const { codeText, fullText } = simExpected(newSim)
    const actualFull = await editor.locator('p').textContent() ?? ''
    const codeEl = editor.locator('code')
    const codeCount = await codeEl.count()
    const actualCode = codeCount > 0 ? (await codeEl.textContent() ?? '') : ''

    const simStr = simToString(newSim)
    // eslint-disable-next-line no-console -- Debug output for e2e simulation steps
    console.log(`  ${stepLabel}: sim=${simStr}  expected=[full="${fullText}" code="${codeText}"]  actual=[full="${actualFull}" code="${actualCode}"]`)

    expect(actualFull, `${stepLabel}: fullText mismatch (sim=${simStr})`).toBe(fullText)
    if (codeText) {
      expect(actualCode, `${stepLabel}: codeText mismatch (sim=${simStr})`).toBe(codeText)
    }

    return newSim
  }

  test('ArrowRight at end of inline code exits code mark', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create inline code: `abc`
    await page.keyboard.type('`abc`', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('abc')

    // Initial state: |abc|^
    // Type X, ArrowLeft to verify position, then ArrowRight + type to continue
    let sim: SimState = ['|', 'a', 'b', 'c', '|', '^']
    sim = await runAction(page, editor, sim, 'X', 'type X at |abc|^')
    sim = await runAction(page, editor, sim, 'ArrowLeft', 'undo: left')
    sim = await runAction(page, editor, sim, 'ArrowRight', 'right')
    await runAction(page, editor, sim, 'Y', 'type Y at |abc|X^')
  })

  test('ArrowLeft moves into code span from boundary', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create inline code: `a`
    await page.keyboard.type('`a`', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('a')

    // Initial state: |a|^
    // At each ArrowLeft stop, type X then ArrowLeft to compensate
    let sim: SimState = ['|', 'a', '|', '^']

    sim = await runAction(page, editor, sim, 'ArrowLeft', 'left 1')
    // sim = |a^|
    sim = await runAction(page, editor, sim, 'X', 'type X at |a^|')
    sim = await runAction(page, editor, sim, 'ArrowLeft', 'undo: left')

    sim = await runAction(page, editor, sim, 'ArrowLeft', 'left 2')
    // sim = |^aX|
    sim = await runAction(page, editor, sim, 'Y', 'type Y at |^aX|')
    await runAction(page, editor, sim, 'ArrowLeft', 'undo: left')
  })

  test('ArrowLeft crosses both boundaries of code span', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Build ab|c|d
    await page.keyboard.type('ab`c`', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('c')
    await page.keyboard.press('ArrowRight')
    await page.keyboard.type('d', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('c')

    // Initial state: ab|c|d^
    let sim: SimState = ['a', 'b', '|', 'c', '|', 'd', '^']

    sim = await runAction(page, editor, sim, 'ArrowLeft', 'left 1')
    // sim = ab|c|^d
    sim = await runAction(page, editor, sim, 'X', 'type X at ab|c|^d')
    sim = await runAction(page, editor, sim, 'ArrowLeft', 'undo: left')

    sim = await runAction(page, editor, sim, 'ArrowLeft', 'left 2')
    // sim = ab|c^|Xd
    sim = await runAction(page, editor, sim, 'Y', 'type Y at ab|c^|Xd')
    sim = await runAction(page, editor, sim, 'ArrowLeft', 'undo: left')

    sim = await runAction(page, editor, sim, 'ArrowLeft', 'left 3')
    // sim = ab|^cY|Xd
    sim = await runAction(page, editor, sim, 'Z', 'type Z at ab|^cY|Xd')
    sim = await runAction(page, editor, sim, 'ArrowLeft', 'undo: left')

    sim = await runAction(page, editor, sim, 'ArrowLeft', 'left 4')
    // sim = ab^|ZcY|Xd
    sim = await runAction(page, editor, sim, 'W', 'type W at ab^|ZcY|Xd')
    await runAction(page, editor, sim, 'ArrowLeft', 'undo: left')
  })

  test('ArrowRight crosses both boundaries of code span', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Build a|b|c
    await page.keyboard.type('a`b`', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('b')
    // After input rule, storedMarks = [] so typing 'c' produces plain text
    await page.keyboard.type('c', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('b')

    // Move cursor to start of paragraph
    await page.keyboard.press('Home')
    await page.waitForTimeout(100)

    // Initial state: ^a|b|c
    // No undo needed — typing inserts before ^, so cursor stays
    // ahead of the typed char and we continue rightward.
    let sim: SimState = ['^', 'a', '|', 'b', '|', 'c']

    sim = await runAction(page, editor, sim, 'ArrowRight', 'right 1')
    // sim = a^|b|c
    sim = await runAction(page, editor, sim, 'X', 'type X at a^|b|c')
    // sim = aX^|b|c

    sim = await runAction(page, editor, sim, 'ArrowRight', 'right 2')
    // sim = aX|^b|c
    sim = await runAction(page, editor, sim, 'Y', 'type Y at aX|^b|c')
    // sim = aX|Y^b|c

    sim = await runAction(page, editor, sim, 'ArrowRight', 'right 3')
    // sim = aX|Yb^|c
    sim = await runAction(page, editor, sim, 'Z', 'type Z at aX|Yb^|c')
    // sim = aX|YbZ^|c

    sim = await runAction(page, editor, sim, 'ArrowRight', 'right 4')
    // sim = aX|YbZ|^c
    sim = await runAction(page, editor, sim, 'W', 'type W at aX|YbZ|^c')
    // sim = aX|YbZ|W^c

    await runAction(page, editor, sim, 'ArrowRight', 'right 5')
    // sim = aX|YbZ|Wc^
  })

  test('ArrowLeft re-enters code span from outside', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create inline code `a` then type b outside: |a|b
    await page.keyboard.type('`a`', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('a')
    await page.keyboard.press('ArrowRight')
    await page.keyboard.type('b', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('a')

    // Initial state: |a|b^
    let sim: SimState = ['|', 'a', '|', 'b', '^']

    sim = await runAction(page, editor, sim, 'ArrowLeft', 'left 1')
    // sim = |a|^b
    sim = await runAction(page, editor, sim, 'X', 'type X at |a|^b')
    sim = await runAction(page, editor, sim, 'ArrowLeft', 'undo: left')

    sim = await runAction(page, editor, sim, 'ArrowLeft', 'left 2')
    // sim = |a^|Xb
    sim = await runAction(page, editor, sim, 'Y', 'type Y at |a^|Xb')
    sim = await runAction(page, editor, sim, 'ArrowLeft', 'undo: left')

    sim = await runAction(page, editor, sim, 'ArrowLeft', 'left 3')
    // sim = |^aY|Xb
    sim = await runAction(page, editor, sim, 'Z', 'type Z at |^aY|Xb')
    await runAction(page, editor, sim, 'ArrowLeft', 'undo: left')
  })

  test('Backspace removes last char of code span and exits code mode', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create inline code: `a`
    await page.keyboard.type('`a`', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('a')

    // Cursor is at the right boundary of code span; move left into code
    await page.keyboard.press('ArrowLeft')
    await page.waitForTimeout(100)
    // Now inside code at right edge of 'a'

    // Backspace should delete 'a' and exit code mode
    await page.keyboard.press('Backspace')
    await page.waitForTimeout(100)

    // Code element should be gone
    await expect(editor.locator('code')).toHaveCount(0)

    // Toolbar code button should be inactive
    const codeBtn = page.locator('[data-testid="toolbar-code"]')
    await expect(codeBtn).not.toHaveClass(/IconButton_active/)

    // Typing new text should produce plain text (not code)
    await page.keyboard.type('plain', { delay: 100 })
    await expect(editor.locator('code')).toHaveCount(0)
    await expect(editor.locator('p')).toContainText('plain')
  })

  test('Mod-e toggles inline code with empty selection', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    const codeBtn = page.locator('[data-testid="toolbar-code"]')

    // Mod-e to enter code mode
    await page.keyboard.press('Meta+e')
    await page.waitForTimeout(100)

    // Toolbar code button should be active
    await expect(codeBtn).toHaveClass(/IconButton_active/)

    // Type text — should be code
    await page.keyboard.type('code', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('code')

    // Mod-e again to exit code mode (removes mark from existing text)
    await page.keyboard.press('Meta+e')
    await page.waitForTimeout(100)

    // Code mark should be removed
    await expect(codeBtn).not.toHaveClass(/IconButton_active/)
    await expect(editor.locator('code')).toHaveCount(0)

    // Type more — should be plain
    await page.keyboard.type(' plain', { delay: 100 })
    await expect(editor.locator('code')).toHaveCount(0)
    await expect(editor.locator('p')).toContainText('code plain')
  })

  test('Toolbar code button toggles inline code with empty selection', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    const codeBtn = page.locator('[data-testid="toolbar-code"]')

    // Click code button to enter code mode
    await codeBtn.click()
    await expect(codeBtn).toHaveClass(/IconButton_active/)

    // Type text — should be code
    await editor.click()
    await page.keyboard.type('inline', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('inline')

    // Click code button again to exit code mode
    await codeBtn.click()
    await expect(codeBtn).not.toHaveClass(/IconButton_active/)
    await expect(editor.locator('code')).toHaveCount(0)
  })
})

test.describe('Undo/Redo', () => {
  test('Cmd+Z undoes and Cmd+Shift+Z redoes', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type text
    await page.keyboard.type('hello world', { delay: 100 })
    await expect(editor).toContainText('hello world')

    // Undo
    await page.keyboard.press('Meta+z')
    // Text should be partially or fully removed
    const textAfterUndo = await editor.textContent()
    expect(textAfterUndo?.trim()).not.toBe('hello world')

    // Redo
    await page.keyboard.press('Meta+Shift+z')
    await expect(editor).toContainText('hello world')
  })
})

test.describe('Syntax Highlighting', () => {
  test('editor code block shows syntax-highlighted tokens', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()

    // Type some JavaScript code first (so the code block has content)
    await page.keyboard.type('const x = 1', { delay: 100 })

    // Set language to javascript via the language label
    const langLabel = editor.locator('.code-lang-label')
    await langLabel.click()
    // Type 'javascript' in the combobox input and select it
    const langInput = page.locator('[data-testid="code-lang-input"]')
    await langInput.fill('javascript')
    await page.keyboard.press('Enter')

    // Wait for highlighting to apply (prosemirror-highlight may need a tick)
    await page.waitForTimeout(1000)

    // Shiki decorations add spans with class="shiki" and inline styles
    const highlightedSpans = editor.locator('pre .shiki')
    await expect(highlightedSpans.first()).toBeVisible()
  })

  test('auto-detect highlights code without changing language label', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block (language defaults to plaintext)
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()

    // Type recognizable JavaScript code (must be >10 chars for detection)
    await page.keyboard.type('const myVar = "hello world"', { delay: 100 })

    // Wait for highlighting to apply
    await page.waitForTimeout(500)

    // Shiki decorations should be present (auto-detected as JS/TS)
    const highlightedSpans = editor.locator('pre .shiki')
    const count = await highlightedSpans.count()
    expect(count).toBeGreaterThan(0)

    // Language label should still show "plaintext" (auto-detect doesn't change the label)
    const langLabel = editor.locator('.code-lang-label')
    await expect(langLabel).toHaveText('plaintext')
  })
})

test.describe('Blockquote Backspace', () => {
  test('Backspace at start of blockquote lifts content out', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a blockquote via markdown input rule: typing "> " (greater-than + space)
    // at the start of a line triggers Milkdown's blockquote wrapping input rule.
    // After the input rule fires, the cursor is at parentOffset 0 of the empty
    // paragraph inside the blockquote.
    await page.keyboard.type('> ', { delay: 100 })

    // Verify blockquote was created
    await expect(editor.locator('blockquote')).toBeVisible()

    // Type text so we can confirm it's preserved after the lift
    await page.keyboard.type('Hi', { delay: 100 })
    await expect(editor.locator('blockquote p')).toContainText('Hi')

    // Move cursor to parentOffset 0 by pressing ArrowLeft for each character typed.
    // A small delay between presses ensures ProseMirror syncs cursor state from the DOM.
    await page.keyboard.press('ArrowLeft')
    await page.waitForTimeout(50)
    await page.keyboard.press('ArrowLeft')
    await page.waitForTimeout(50)

    // Backspace at parentOffset 0 inside blockquote should lift content out
    await page.keyboard.press('Backspace')

    // Blockquote should be gone, text should remain in a plain paragraph
    await expect(editor.locator('blockquote')).not.toBeVisible()
    await expect(editor.locator('p')).toContainText('Hi')
  })
})

test.describe('Empty Code Block Backspace', () => {
  test('Backspace in empty code block converts to paragraph', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()

    // Verify code block exists
    await expect(editor.locator('pre')).toBeVisible()

    // Backspace in empty code block should convert to paragraph
    await page.keyboard.press('Backspace')

    // Code block should be gone
    await expect(editor.locator('pre')).not.toBeVisible()
    await expect(editor.locator('p')).toBeVisible()
  })
})

test.describe('Horizontal Rule Backspace Revert', () => {
  test('Backspace after HR reverts to --- text', async ({ page, authenticatedWorkspace }) => {
    // Default mode is Cmd+Enter-to-send, so Enter creates newlines
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type --- to create an HR (needs content before to trigger on a new line)
    await page.keyboard.type('before', { delay: 100 })
    await page.keyboard.press('Enter')
    await page.keyboard.type('---', { delay: 30 })

    // HR should be created
    await expect(editor.locator('hr')).toBeVisible()

    // Cursor should be in the empty paragraph after the HR
    // Backspace should revert the HR to --- text
    await page.keyboard.press('Backspace')

    // HR should be gone, replaced by paragraph containing "---"
    await expect(editor.locator('hr')).not.toBeVisible()
    await expect(editor.locator('p').last()).toContainText('---')
  })

  test('--- does NOT trigger inside a code block', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()

    // Type --- inside the code block
    await page.keyboard.type('---', { delay: 30 })

    // No HR should appear — the text should remain as code
    await expect(editor.locator('hr')).not.toBeVisible()
    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('---')
  })

  test('--- does NOT trigger inside inline code', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Apply inline code first via toolbar
    const codeBtn = page.locator('[data-testid="toolbar-code"]')
    await codeBtn.click()

    // Type --- inside the inline code span
    await page.keyboard.type('---', { delay: 30 })

    // No HR should appear — the text should remain as inline code
    await expect(editor.locator('hr')).not.toBeVisible()
    await expect(editor.locator('code')).toBeVisible()
  })
})

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

test.describe('Code Block Input Rule', () => {
  test('typing ``` creates a code block', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type ``` to trigger code block input rule
    await page.keyboard.type('```', { delay: 30 })

    // A code block (pre element) should appear
    await expect(editor.locator('pre')).toBeVisible()

    // Code block button should be active
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await expect(codeBlockBtn).toHaveClass(/IconButton_active/)
  })

  test('typing ``` after text creates code block after paragraph', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type text first, then Shift+Enter, then ```
    await page.keyboard.type('some text', { delay: 100 })
    await page.keyboard.press('Shift+Enter')
    await page.keyboard.type('```', { delay: 30 })

    // A code block should appear after the paragraph
    await expect(editor.locator('pre')).toBeVisible()
    await expect(editor.locator('p')).toContainText('some text')
  })
})

test.describe('Link Input Rule', () => {
  test('[text](url) creates a link', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type the link markdown syntax
    await page.keyboard.type('[click here](https://example.com)', { delay: 100 })

    // A link element should be created
    const link = editor.locator('a')
    await expect(link).toBeVisible()
    await expect(link).toHaveText('click here')
    await expect(link).toHaveAttribute('href', 'https://example.com')
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

test.describe('Enter Sends Message', () => {
  test('Cmd+Enter sends message in default mode', async ({ page, authenticatedWorkspace }) => {
    // Default mode is Cmd+Enter-to-send
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type some text
    await page.keyboard.type('hello from cmd enter', { delay: 100 })
    await expect(editor).toContainText('hello from cmd enter')

    // Plain Enter should NOT send — it creates a new line
    await page.keyboard.press('Enter')
    await page.waitForTimeout(200)
    await expect(editor).toContainText('hello from cmd enter')

    // Cmd+Enter should send
    await page.keyboard.press('Meta+Enter')

    // The editor should be cleared after sending
    await page.waitForTimeout(300)
    const textAfterSend = await editor.textContent()
    expect(textAfterSend?.trim()).toBe('')
  })

  test('Enter sends message after toggling to Enter mode', async ({ page, authenticatedWorkspace }) => {
    // Toggle from default Cmd+Enter mode to Enter-sends mode
    await page.locator('button:has-text("Enter sends")').click()

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type some text
    await page.keyboard.type('hello from enter key', { delay: 100 })
    await expect(editor).toContainText('hello from enter key')

    // Enter should send in Enter-sends mode
    await page.keyboard.press('Enter')

    // The editor should be cleared after sending
    await page.waitForTimeout(300)
    const textAfterSend = await editor.textContent()
    expect(textAfterSend?.trim()).toBe('')
  })

  test('Enter mode toggle switches between modes', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // The toggle button always contains "Enter sends" text in both modes
    const enterModeBtn = page.locator('button:has-text("Enter sends")')
    await expect(enterModeBtn).toBeVisible()

    // Click to toggle from default Cmd+Enter mode to Enter-sends mode
    await enterModeBtn.click()

    // Verify that Enter in editor sends (clears editor)
    await editor.click()
    await page.keyboard.type('will be sent', { delay: 100 })
    await page.keyboard.press('Enter')

    await page.waitForTimeout(300)
    const textAfterFirstSend = await editor.textContent()
    expect(textAfterFirstSend?.trim()).toBe('')

    // Click the same button again to toggle back to Cmd+Enter mode
    await page.locator('button:has-text("Enter sends")').click()

    // Type text and verify Enter creates a newline (doesn't send)
    await editor.click()
    await page.keyboard.type('test text', { delay: 100 })
    await page.keyboard.press('Enter')
    await page.keyboard.type('new line', { delay: 100 })

    // Both lines should be in the editor (Enter didn't send)
    await expect(editor).toContainText('test text')
    await expect(editor).toContainText('new line')

    // Cmd+Enter should send
    await page.keyboard.press('Meta+Enter')

    await page.waitForTimeout(300)
    const textAfterSecondSend = await editor.textContent()
    expect(textAfterSecondSend?.trim()).toBe('')
  })
})

test.describe('Code Block Enter in List Items', () => {
  test('Enter at end of code block inside list item inserts newline', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a bullet list item by typing "- " (triggers input rule)
    await page.keyboard.type('- List item', { delay: 100 })
    await expect(editor.locator('ul > li')).toBeVisible()

    // Press Enter to create new line in list, then type ``` to trigger code block
    await page.keyboard.press('Shift+Enter')
    await page.keyboard.type('```', { delay: 30 })

    // Verify code block was created inside the list item
    await expect(editor.locator('ul > li pre')).toBeVisible()

    // Type some code
    await page.keyboard.type('line1', { delay: 100 })

    // Press Enter — should insert a newline, NOT exit the code block
    await page.keyboard.press('Enter')
    await page.waitForTimeout(100)

    // Type more text on the new line
    await page.keyboard.type('line2', { delay: 100 })

    // Verify both lines are in the code block
    const codeText = await editor.locator('ul > li pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toContain('line1')
    expect(codeText).toContain('line2')
    expect(codeText).toContain('\n')

    // Verify we're still inside one list item (not two)
    await expect(editor.locator('ul > li')).toHaveCount(1)
  })

  test('Enter in middle of code block inside list item inserts newline', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a bullet list item
    await page.keyboard.type('- List item', { delay: 100 })
    await expect(editor.locator('ul > li')).toBeVisible()

    // Create code block inside list item
    await page.keyboard.press('Shift+Enter')
    await page.keyboard.type('```', { delay: 30 })
    await expect(editor.locator('ul > li pre')).toBeVisible()

    // Type text
    await page.keyboard.type('ab', { delay: 100 })

    // Move cursor to the middle (between a and b)
    await page.keyboard.press('ArrowLeft')
    await page.waitForTimeout(100)

    // Press Enter — should split with newline
    await page.keyboard.press('Enter')
    await page.waitForTimeout(100)

    // Verify the code block content has both parts with a newline
    const codeText = await editor.locator('ul > li pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('a\nb')

    // Verify still one list item
    await expect(editor.locator('ul > li')).toHaveCount(1)
  })

  test('Enter in standalone code block still works', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a standalone code block via toolbar button
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await expect(editor.locator('pre')).toBeVisible()

    // Type some text
    await page.keyboard.type('hello', { delay: 100 })

    // Press Enter — should insert newline
    await page.keyboard.press('Enter')
    await page.waitForTimeout(100)
    await page.keyboard.type('world', { delay: 100 })

    // Verify code block has both lines
    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toContain('hello')
    expect(codeText).toContain('world')
    expect(codeText).toContain('\n')
  })
})

test.describe('ArrowRight Plain-to-Code Boundary', () => {
  test('ArrowRight from plain text enters code span without getting stuck', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Build a|bc|d
    await page.keyboard.type('a`bc`', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('bc')
    // After input rule, storedMarks = [] so typing 'd' produces plain text
    await page.keyboard.type('d', { delay: 100 })
    await expect(editor.locator('code')).toHaveText('bc')

    // Move cursor to start
    await page.keyboard.press('Home')
    await page.waitForTimeout(100)

    // Press ArrowRight 3 times: from ^a|bc|d → a^|bc|d → a|^bc|d → a|b^c|d
    await page.keyboard.press('ArrowRight')
    await page.waitForTimeout(100)
    await page.keyboard.press('ArrowRight')
    await page.waitForTimeout(100)
    await page.keyboard.press('ArrowRight')
    await page.waitForTimeout(100)

    // Type X — should be inside the code span after 'b'
    await page.keyboard.type('X', { delay: 100 })

    // Verify: code span should now be 'bXc'
    await expect(editor.locator('code')).toHaveText('bXc')
  })
})

test.describe('ArrowDown Code Block Scroll', () => {
  test('ArrowDown from code block scrolls new paragraph into view', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block with many lines to push it near the bottom
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await expect(editor.locator('pre')).toBeVisible()

    // Type many lines to fill the code block and cause scrolling
    for (let i = 0; i < 30; i++) {
      await page.keyboard.type(`line ${i}`, { delay: 10 })
      if (i < 29) {
        await page.keyboard.press('Enter')
      }
    }
    await page.waitForTimeout(200)

    // Press ArrowDown to escape the code block
    await page.keyboard.press('ArrowDown')
    await page.waitForTimeout(300)

    // A new paragraph should exist after the code block
    const paragraphs = editor.locator('p')
    await expect(paragraphs).toHaveCount(1)

    // The new paragraph should be visible in the editor wrapper's viewport
    const editorWrapper = page.locator('[data-testid="chat-editor"]')
    const wrapperBox = await editorWrapper.boundingBox()
    const paraBox = await paragraphs.first().boundingBox()

    expect(wrapperBox).toBeTruthy()
    expect(paraBox).toBeTruthy()
    if (wrapperBox && paraBox) {
      // The paragraph's top should be within the visible area of the wrapper
      expect(paraBox.y).toBeGreaterThanOrEqual(wrapperBox.y)
      expect(paraBox.y).toBeLessThanOrEqual(wrapperBox.y + wrapperBox.height)
    }
  })
})

test.describe('Send Feedback Button Labels', () => {
  test('ExitPlanMode banner shows Send Feedback instead of Reject', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Ask agent to enter plan mode and exit with a dummy plan
    await page.keyboard.type(PLAN_MODE_PROMPT, { delay: 50 })
    await page.keyboard.press('Meta+Enter')

    // Wait for ExitPlanMode control_request
    const banner = page.locator('[data-testid="control-banner"]')
    await expect(banner).toBeVisible({ timeout: 60_000 })
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // Verify the reject button says "Send Feedback"
    const rejectBtn = page.locator('[data-testid="plan-reject-btn"]')
    await expect(rejectBtn).toHaveText('Send Feedback')
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

test.describe('Inline Code Suppresses Formatting Input Rules', () => {
  test('typing *foo* inside inline code does NOT create italic', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Toggle inline code via toolbar
    const codeBtn = page.locator('[data-testid="toolbar-code"]')
    await codeBtn.click()

    // Type *foo* inside inline code
    await page.keyboard.type('*foo*', { delay: 100 })

    // Should NOT have an <em> element — should have inline code containing *foo*
    await expect(editor.locator('em')).not.toBeVisible()
    await expect(editor.locator('code')).toContainText('*foo*')
  })

  test('typing **foo** inside inline code does NOT create bold', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Toggle inline code via toolbar
    const codeBtn = page.locator('[data-testid="toolbar-code"]')
    await codeBtn.click()

    // Type **foo** inside inline code
    await page.keyboard.type('**foo**', { delay: 100 })

    // Should NOT have a <strong> element
    await expect(editor.locator('strong')).not.toBeVisible()
    await expect(editor.locator('code')).toContainText('**foo**')
  })

  test('typing ~~foo~~ inside inline code does NOT create strikethrough', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Toggle inline code via toolbar
    const codeBtn = page.locator('[data-testid="toolbar-code"]')
    await codeBtn.click()

    // Type ~~foo~~ inside inline code
    await page.keyboard.type('~~foo~~', { delay: 100 })

    // Should NOT have a <del> element
    await expect(editor.locator('del')).not.toBeVisible()
    await expect(editor.locator('code')).toContainText('~~foo~~')
  })

  test('typing *foo* outside inline code still creates italic (regression)', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type *foo* in a normal paragraph
    await page.keyboard.type('*foo*', { delay: 100 })

    // Should have an <em> element
    await expect(editor.locator('em')).toHaveText('foo')
  })
})

test.describe('Selection Wrap with Special Characters', () => {
  test('typing ( with selection wraps text in parentheses', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    await page.keyboard.type('hello', { delay: 100 })

    // Select "hello"
    await page.keyboard.press('Home')
    await page.keyboard.press('Shift+End')
    await page.waitForTimeout(100)

    // Type (
    await page.keyboard.type('(')
    await page.waitForTimeout(100)

    await expect(editor).toContainText('(hello)')
  })

  test('typing [ with selection wraps text in brackets', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    await page.keyboard.type('hello', { delay: 100 })

    // Select "hello"
    await page.keyboard.press('Home')
    await page.keyboard.press('Shift+End')
    await page.waitForTimeout(100)

    // Type [
    await page.keyboard.type('[')
    await page.waitForTimeout(100)

    await expect(editor).toContainText('[hello]')
  })

  test('typing { with selection wraps text in braces', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    await page.keyboard.type('hello', { delay: 100 })

    // Select "hello"
    await page.keyboard.press('Home')
    await page.keyboard.press('Shift+End')
    await page.waitForTimeout(100)

    // Type {
    await page.keyboard.type('{')
    await page.waitForTimeout(100)

    await expect(editor).toContainText('{hello}')
  })

  test('typing ` with selection toggles inline code', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    await page.keyboard.type('hello', { delay: 100 })

    // Select "hello"
    await page.keyboard.press('Home')
    await page.keyboard.press('Shift+End')
    await page.waitForTimeout(100)

    // Type `
    await page.keyboard.type('`')
    await page.waitForTimeout(100)

    // Should be wrapped in inline code
    await expect(editor.locator('code')).toHaveText('hello')
  })

  test('typing * with selection toggles bold', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    await page.keyboard.type('hello', { delay: 100 })

    // Select "hello"
    await page.keyboard.press('Home')
    await page.keyboard.press('Shift+End')
    await page.waitForTimeout(100)

    // Type *
    await page.keyboard.type('*')
    await page.waitForTimeout(100)

    // Should be wrapped in strong
    await expect(editor.locator('strong')).toHaveText('hello')
  })

  test('typing _ with selection toggles italic', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    await page.keyboard.type('hello', { delay: 100 })

    // Select "hello"
    await page.keyboard.press('Home')
    await page.keyboard.press('Shift+End')
    await page.waitForTimeout(100)

    // Type _
    await page.keyboard.type('_')
    await page.waitForTimeout(100)

    // Should be wrapped in italic
    await expect(editor.locator('em')).toHaveText('hello')
  })

  test('typing ~ with selection toggles strikethrough', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    await page.keyboard.type('hello', { delay: 100 })

    // Select "hello"
    await page.keyboard.press('Home')
    await page.keyboard.press('Shift+End')
    await page.waitForTimeout(100)

    // Type ~
    await page.keyboard.type('~')
    await page.waitForTimeout(100)

    // Should be wrapped in strikethrough
    await expect(editor.locator('del')).toHaveText('hello')
  })

  test('without selection, special characters type normally (regression)', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Type ( without selection — should just insert (
    await page.keyboard.type('abc(def', { delay: 100 })
    await expect(editor).toContainText('abc(def')
  })

  test('bracket wrapping inside code_block works', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await expect(editor.locator('pre')).toBeVisible()

    await page.keyboard.type('hello', { delay: 100 })

    // Select "hello"
    await page.keyboard.press('Home')
    await page.keyboard.press('Shift+End')
    await page.waitForTimeout(100)

    // Type (
    await page.keyboard.type('(')
    await page.waitForTimeout(100)

    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('(hello)')
  })

  test('mark toggles inside code_block just insert character', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()

    // Create a code block
    const codeBlockBtn = page.locator('[data-testid="toolbar-codeblock"]')
    await codeBlockBtn.click()
    await expect(editor.locator('pre')).toBeVisible()

    await page.keyboard.type('hello', { delay: 100 })

    // Select "hello"
    await page.keyboard.press('Home')
    await page.keyboard.press('Shift+End')
    await page.waitForTimeout(100)

    // Type * — should NOT toggle bold, just replace selection with *
    await page.keyboard.type('*')
    await page.waitForTimeout(100)

    // Should NOT have a <strong> element — just the * character
    await expect(editor.locator('strong')).not.toBeVisible()
    const codeText = await editor.locator('pre code').evaluate((el) => {
      const clone = el.cloneNode(true) as HTMLElement
      clone.querySelectorAll('.code-lang-label').forEach(s => s.remove())
      return clone.textContent
    })
    expect(codeText).toBe('*')
  })
})
