import { expect, test } from './fixtures'
import { openAgentViaUI } from './helpers'

test.describe('TabBar Improvements', () => {
  test('should create a new agent via the agent button', async ({ page, authenticatedWorkspace }) => {
    // Click the new agent button
    await page.locator('[data-testid="new-agent-button"]').click()

    // Verify new agent tab is created
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toBeVisible()
    // Verify the ProseMirror editor is visible (agent is ready)
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()
  })

  test('new agent tab should focus the message editor', async ({ page, authenticatedWorkspace }) => {
    // Open a second agent tab (the first was auto-created with the workspace).
    await openAgentViaUI(page)

    // The ProseMirror editor should be focused.
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await expect(editor).toBeFocused()
  })

  test('should create a new terminal via the terminal button', async ({ page, authenticatedWorkspace }) => {
    // Click the new terminal button
    await page.locator('[data-testid="new-terminal-button"]').click()

    // Verify new terminal tab is created
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()
    // Verify xterm is rendered
    await expect(page.locator('.xterm')).toBeVisible()
  })

  test('should show dropdown with grouped items', async ({ page, authenticatedWorkspace }) => {
    // Open the more menu
    await page.locator('[data-testid="tab-more-menu"]').click()

    // Scope to the visible popover menu (TabBar renders items in multiple responsive menus)
    const openMenu = page.locator('menu[popover]:visible')
    // Verify "Agents" label and "Resume an existing session" item
    await expect(openMenu.getByText('Agents', { exact: true })).toBeVisible()
    await expect(openMenu.getByRole('menuitem', { name: 'Resume an existing session' })).toBeVisible()

    // Verify separator and "Terminals" label are visible
    await expect(openMenu.getByText('Terminals', { exact: true })).toBeVisible()

    // Close the dropdown
    await page.keyboard.press('Escape')
  })

  test('should open terminal with specific shell from dropdown', async ({ page, authenticatedWorkspace }) => {
    // Open the more menu
    await page.locator('[data-testid="tab-more-menu"]').click()

    // Scope to the visible popover menu
    const openMenu = page.locator('menu[popover]:visible')
    // Wait for menu items to load
    await expect(openMenu.getByText('Terminals', { exact: true })).toBeVisible()

    // Find shell menu items (not the "Resume" item)
    const menuItems = openMenu.locator('[role="menuitem"]')
    const count = await menuItems.count()

    // Find a shell item (contains a path like /bin/bash)
    for (let i = 0; i < count; i++) {
      const text = await menuItems.nth(i).textContent()
      if (text && text.includes('/bin/')) {
        await menuItems.nth(i).click()
        // Verify terminal tab is created
        await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()
        await expect(page.locator('.xterm')).toBeVisible()
        return
      }
    }
    // If no shells found, close menu and skip
    await page.keyboard.press('Escape')
  })

  test('should open and validate resume session dialog', async ({ page, authenticatedWorkspace }) => {
    // Open the more menu and click "Resume an existing session"
    await page.locator('[data-testid="tab-more-menu"]').click()
    await page.getByRole('menuitem', { name: 'Resume an existing session' }).click()

    // Verify dialog appears
    await expect(page.locator('[data-testid="resume-session-id-input"]')).toBeVisible()
    await expect(page.locator('[data-testid="resume-session-submit"]')).toBeVisible()

    // Enter invalid characters - verify validation error
    await page.locator('[data-testid="resume-session-id-input"]').fill('invalid chars!@#')
    await expect(page.getByText('Only letters, numbers, dashes, and underscores are allowed')).toBeVisible()

    // Resume button should be disabled with invalid input
    await expect(page.locator('[data-testid="resume-session-submit"]')).toBeDisabled()

    // Enter valid session ID
    await page.locator('[data-testid="resume-session-id-input"]').fill('valid-session-123')
    await expect(page.getByText('Only letters, numbers, dashes')).not.toBeVisible()

    // Resume button should be enabled
    await expect(page.locator('[data-testid="resume-session-submit"]')).toBeEnabled()

    // Cancel the dialog
    await page.getByRole('button', { name: 'Cancel' }).click()
    await expect(page.locator('[data-testid="resume-session-id-input"]')).not.toBeVisible()
  })

  test('should display session ID in ChatView footer after agent starts', async ({ page, authenticatedWorkspace }) => {
    // Open a new agent and send a message
    await openAgentViaUI(page)

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('Say "hello". Reply with just the word, nothing else.')
    await page.keyboard.press('Enter')

    // Wait for the agent to respond (which triggers the init message with session ID)
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('hello') && !body.includes('Send a message to start')
    })

    // The session ID trigger button should now be visible in the footer
    await expect(page.locator('[data-testid="session-id-trigger"]')).toBeVisible()

    // Click to open the popover
    await page.locator('[data-testid="session-id-trigger"]').click()

    // Verify popover shows session ID
    await expect(page.locator('[data-testid="session-id-popover"]')).toBeVisible()
    await expect(page.locator('[data-testid="session-id-value"]')).toBeVisible()

    // Session ID should be non-empty
    const sessionIdText = await page.locator('[data-testid="session-id-value"]').textContent()
    expect(sessionIdText).toBeTruthy()
    expect(sessionIdText!.length).toBeGreaterThan(0)

    // Copy button should be visible
    await expect(page.locator('[data-testid="session-id-copy"]')).toBeVisible()
  })

  test('should not create a new agent when double-clicking a tab to rename', async ({ page, authenticatedWorkspace }) => {
    // Count initial agent tabs
    const initialCount = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()

    // Double-click the tab to start renaming
    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
    await agentTab.dblclick()

    // The rename input should appear
    const editInput = agentTab.locator('input')
    await expect(editInput).toBeVisible()
    await expect(editInput).toBeFocused()

    // Cancel the rename
    await editInput.press('Escape')
    await expect(editInput).not.toBeVisible()

    // Wait to ensure no asynchronous agent creation is triggered
    await page.waitForTimeout(3000)

    // No new agent tab should have been created
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(initialCount)
  })

  test('should create a new agent by double-clicking empty tab bar area', async ({ page, authenticatedWorkspace }) => {
    // Count initial agent tabs
    const initialCount = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()

    // Double-click the empty area in the tab list (not on a tab)
    const tabList = page.locator('[data-testid="tab-list"]')
    const box = await tabList.boundingBox()
    await tabList.dblclick({ position: { x: box!.width - 10, y: box!.height / 2 } })

    // Verify a new agent tab is created
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(initialCount + 1)
    // Verify the ProseMirror editor is visible (agent is ready)
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()
  })
})
