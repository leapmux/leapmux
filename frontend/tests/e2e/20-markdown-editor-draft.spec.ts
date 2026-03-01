import { expect, test } from './fixtures'
import { openAgentViaUI } from './helpers/ui'

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
