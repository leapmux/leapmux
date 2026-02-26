import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

/**
 * Ensure at least one agent tab exists after workspace creation
 * and wait for the tab count to stabilize (auto-created agents from
 * the worker may arrive asynchronously after workspace creation).
 * Returns the settled agent tab count.
 */
async function ensureAgentTab(page: Page): Promise<number> {
  try {
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toBeVisible()
  }
  catch {
    await page.locator('[data-testid="new-agent-button"]').click()
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toBeVisible()
  }
  // Wait for auto-created agents from the worker to settle.
  await page.waitForTimeout(2000)
  return page.locator('[data-testid="tab"][data-tab-type="agent"]').count()
}

test.describe('Workspace Chat', () => {
  test('should create workspace, open agent, and receive response from Claude', async ({ page, authenticatedWorkspace }) => {
    // An agent tab is auto-created when a workspace is created.
    // Wait for the Milkdown editor to be ready.
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message to Claude via the rich text editor
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')

    // Editor should be cleared after sending
    await expect(editor).toHaveText('')

    // Wait for Claude's response: the page should contain "4" (the answer)
    // and the empty state should be gone.
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('4') && !body.includes('Send a message to start')
    })
  })

  test('should show workspace in sidebar after creation', async ({ page, authenticatedWorkspace }) => {
    // Workspace should be visible in the sidebar (fixture auto-creates workspace)
    await expect(page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)).toBeVisible()
  })

  test('should rename a tab via double-click', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()

    // Double-click the tab to enter edit mode
    await agentTab.dblclick()

    // An inline text input should appear inside the tab
    const editInput = agentTab.locator('input')
    await expect(editInput).toBeVisible()
    await expect(editInput).toBeFocused()

    // Clear and type a new name
    await editInput.fill('My Custom Agent')
    await editInput.press('Enter')

    // Input should disappear and tab should show the new name
    await expect(editInput).not.toBeVisible()
    await expect(agentTab).toContainText('My Custom Agent')
  })

  test('should cancel tab rename on Escape', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()

    // Double-click to start editing
    await agentTab.dblclick()
    const editInput = agentTab.locator('input')
    await expect(editInput).toBeVisible()

    // Type something different then press Escape
    await editInput.fill('Should Not Save')
    await editInput.press('Escape')

    // Input should disappear and tab text should remain unchanged
    await expect(editInput).not.toBeVisible()
    await expect(agentTab).not.toContainText('Should Not Save')
    // Verify the tab still has its original title (positive assertion)
    await expect(agentTab).toContainText('Agent')
  })

  test('should show dropdown menu when clicking the more button', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    // Click the dropdown arrow button
    await page.locator('[data-testid="tab-more-menu"]').click()

    // Verify the dropdown menu appears with grouped items.
    // TabBar renders the menu items in multiple responsive menus (full, collapsed, micro),
    // so scope assertions to the visible popover.
    const openMenu = page.locator('menu[popover]:visible')
    await expect(openMenu.getByText('Agents', { exact: true })).toBeVisible()
    await expect(openMenu.getByRole('menuitem', { name: 'Resume an existing session' })).toBeVisible()
    await expect(openMenu.getByText('Terminals', { exact: true })).toBeVisible()

    // Click a shell item to create a terminal tab
    const menuItems = openMenu.locator('[role="menuitem"]')
    const count = await menuItems.count()
    let clickedShell = false
    for (let i = 0; i < count; i++) {
      const text = await menuItems.nth(i).textContent()
      if (text && text.includes('/bin/')) {
        await menuItems.nth(i).click()
        clickedShell = true
        break
      }
    }

    if (clickedShell) {
      // A terminal tab should appear
      await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()
    }
    else {
      // Close the menu if no shells available
      await page.keyboard.press('Escape')
    }
  })

  test('should create agent directly when clicking the agent button', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    // Create a second agent via the agent button
    await page.locator('[data-testid="new-agent-button"]').click()

    // Should now have 2 agent tabs
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)
  })

  test('should close dropdown when clicking outside', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    // Open the dropdown
    await page.locator('[data-testid="tab-more-menu"]').click()
    await expect(page.getByRole('menuitem', { name: 'Resume an existing session' })).toBeVisible()

    // Press Escape to dismiss the dropdown
    await page.keyboard.press('Escape')

    // Dropdown should be closed
    await expect(page.getByRole('menuitem', { name: 'Resume an existing session' })).not.toBeVisible()
  })

  test('should truncate long tab titles', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()

    // Rename the tab to a very long title
    await agentTab.dblclick()
    const editInput = agentTab.locator('input')
    await expect(editInput).toBeVisible()
    await editInput.fill('This Is A Very Long Tab Title That Should Be Truncated')
    await editInput.press('Enter')

    // The tab should contain the text
    await expect(agentTab).toContainText('This Is A Very Long Tab Title')

    // The tab element width should be capped (maxWidth: 200px in TabBar.css.ts)
    const tabWidth = await agentTab.evaluate(el => el.getBoundingClientRect().width)
    expect(tabWidth).toBeLessThanOrEqual(200)
  })

  test('should wrap tabs to multiple rows when many tabs are open', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    // Create 5 more agents to ensure wrapping.
    // Wait for each tab to appear before clicking the agent button again.
    const allTabs = page.locator('[data-testid="tab"]')
    for (let i = 0; i < 5; i++) {
      const countBefore = await allTabs.count()
      await page.locator('[data-testid="new-agent-button"]').click()
      await expect(allTabs).toHaveCount(countBefore + 1)
    }

    // Shrink viewport so tabs must wrap.
    // With resizable panels + drag handles, center area is ~55-60% of viewport width.
    // At 800px: center ~ 440-480px. 6 tabs * ~74px = 444px -> wraps.
    await page.setViewportSize({ width: 800, height: 600 })
    await page.waitForTimeout(500)

    // The agent button should still be visible
    await expect(page.locator('[data-testid="new-agent-button"]')).toBeVisible()

    // The tab bar should have grown taller (multi-row) compared to single-row height
    const tabBarHeight = await page.locator('[data-testid="tab-bar"]').evaluate(el => el.getBoundingClientRect().height)
    // Single row is minHeight 35px (headerHeightPx - 1); with wrapping it should be at least that
    expect(tabBarHeight).toBeGreaterThanOrEqual(35)
  })

  test('should allow double-click rename on a non-active tab', async ({ page, authenticatedWorkspace }) => {
    const initialCount = await ensureAgentTab(page)

    // Create a new agent tab
    await page.locator('[data-testid="new-agent-button"]').click()
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await expect(agentTabs).toHaveCount(initialCount + 1)

    // The newest tab should be active (just created). Click the first tab to make it active.
    await agentTabs.first().click()

    // Double-click the last (non-active) tab to start renaming
    const lastIdx = await agentTabs.count() - 1
    await agentTabs.nth(lastIdx).dblclick()

    // The rename input should appear and be focused
    const editInput = agentTabs.nth(lastIdx).locator('input')
    await expect(editInput).toBeVisible()
    await expect(editInput).toBeFocused()

    // Type a new name and confirm
    await editInput.fill('Renamed Non-Active')
    await editInput.press('Enter')

    // Input should disappear and tab should show the new name
    await expect(editInput).not.toBeVisible()
    await expect(agentTabs.nth(lastIdx)).toContainText('Renamed Non-Active')
  })

  test('should close a tab on middle-click', async ({ page, authenticatedWorkspace }) => {
    const initialCount = await ensureAgentTab(page)

    // Create a new agent tab
    await page.locator('[data-testid="new-agent-button"]').click()
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await expect(agentTabs).toHaveCount(initialCount + 1)

    const countBefore = await agentTabs.count()

    // Middle-click the last agent tab to close it.
    // Use evaluate() to dispatch a proper MouseEvent with button=1,
    // because Playwright's dispatchEvent() may create a generic Event
    // (where e.button is undefined) and click({ button: 'middle' })
    // can be unreliable within DnD-sortable containers.
    await agentTabs.nth(countBefore - 1).evaluate((el) => {
      el.dispatchEvent(new MouseEvent('auxclick', { button: 1, bubbles: true, cancelable: true }))
    })

    // The closed agent tab should be removed
    await expect(agentTabs).toHaveCount(countBefore - 1)
  })
})
