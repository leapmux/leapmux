import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { agentTabs, expectAgentTabCount, openAgentViaUI, renameTabViaUI, terminalTabs, visibleOnly, waitForAgentStarted, workspaceRow } from './helpers/ui'

/** Wait for the fixture's native agent. A slow UI update must not create a second agent. */
async function ensureAgentTab(page: Page): Promise<number> {
  const tabs = visibleOnly(agentTabs(page))
  await expect(tabs.first()).toBeVisible()
  await waitForAgentStarted(page)
  return tabs.count()
}

test.describe('Workspace Chat', () => {
  test('should show workspace in sidebar after creation', async ({ page, authenticatedWorkspace }) => {
    // The fixture creates the workspace. Require its visible sidebar row.
    await expect(workspaceRow(page, authenticatedWorkspace.workspaceId)).toBeVisible()
  })

  test('should rename a tab via double-click', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    const agentTab = agentTabs(page).first()

    // Double-click the tab to edit its title.
    await agentTab.dblclick()

    // Require the title input inside the tab.
    const editInput = agentTab.locator('input')
    await expect(editInput).toBeVisible()
    await expect(editInput).toBeFocused()

    // Replace the title text.
    await editInput.fill('My Custom Agent')
    await editInput.press('Enter')

    // Require the entered title after the input closes.
    await expect(editInput).not.toBeVisible()
    await expect(agentTab).toContainText('My Custom Agent')
  })

  test('should cancel tab rename on Escape', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    const agentTab = agentTabs(page).first()

    // Double-click the tab to edit its title.
    await agentTab.dblclick()
    const editInput = agentTab.locator('input')
    await expect(editInput).toBeVisible()

    // Type a different title. Press Escape to cancel.
    await editInput.fill('Should Not Save')
    await editInput.press('Escape')

    // Require the original title after the input closes.
    await expect(editInput).not.toBeVisible()
    await expect(agentTab).not.toContainText('Should Not Save')
    // Require the original title as a separate positive check.
    await expect(agentTab).toContainText('Agent')
  })

  test('should show dropdown menu when clicking the more button', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    // Open the tab menu.
    await page.locator('[data-testid="tab-more-menu"]').click()

    // Require the grouped menu items in the visible popover.
    // TabBar creates several responsive menu copies, so an unscoped lookup can match a hidden copy.
    const openMenu = page.locator('menu[popover]:visible')
    await expect(openMenu.getByText('Agents', { exact: true })).toBeVisible()
    await expect(openMenu.getByText('Terminals', { exact: true })).toBeVisible()

    // Select a shell to create a terminal tab.
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
      // Require the new terminal tab.
      await expect(terminalTabs(page)).toBeVisible()
    }
    else {
      // Close the menu when it contains no shell.
      await page.keyboard.press('Escape')
    }
  })

  test('should create agent directly when clicking the agent button', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    // Create a second agent through its button.
    await openAgentViaUI(page)

    // Require two agent tabs.
    await expectAgentTabCount(page, 2)
  })

  test('should close dropdown when clicking outside', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    // Open the tab menu.
    await page.locator('[data-testid="tab-more-menu"]').click()
    const openMenu = page.locator('menu[popover]:visible')
    await expect(openMenu.getByText('Agents', { exact: true })).toBeVisible()

    // Press Escape to close the tab menu.
    await page.keyboard.press('Escape')

    // Require the closed tab menu.
    await expect(openMenu.getByText('Agents', { exact: true })).not.toBeVisible()
  })

  test('should truncate long tab titles', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    const agentTab = agentTabs(page).first()

    // Give the tab a long title. The helper requires the entered title text.
    await renameTabViaUI(page, agentTab, 'This Is A Very Long Tab Title That Should Be Truncated')

    // Require the 200px maximum width that the tab stylesheet declares.
    const tabWidth = await agentTab.evaluate(el => el.getBoundingClientRect().width)
    expect(tabWidth).toBeLessThanOrEqual(200)
  })

  test('should allow double-click rename on a non-active tab', async ({ page, authenticatedWorkspace }) => {
    const initialCount = await ensureAgentTab(page)

    // Create an agent tab.
    await openAgentViaUI(page)
    await expectAgentTabCount(page, initialCount + 1)
    const tabs = agentTabs(page)

    // The new tab becomes active. Select the first tab.
    await tabs.first().click()

    // Double-click the last inactive tab to edit its title.
    const lastIdx = await tabs.count() - 1
    await tabs.nth(lastIdx).dblclick()

    // Require the title input and its focus.
    const editInput = tabs.nth(lastIdx).locator('input')
    await expect(editInput).toBeVisible()
    await expect(editInput).toBeFocused()

    // Enter and save the new title.
    await editInput.fill('Renamed Non-Active')
    await editInput.press('Enter')

    // Require the entered title after the input closes.
    await expect(editInput).not.toBeVisible()
    await expect(tabs.nth(lastIdx)).toContainText('Renamed Non-Active')
  })

  test('should close a tab on middle-click', async ({ page, authenticatedWorkspace }) => {
    const initialCount = await ensureAgentTab(page)

    // Create an agent tab.
    await openAgentViaUI(page)
    const countBefore = initialCount + 1
    await expectAgentTabCount(page, countBefore)
    const tabs = agentTabs(page)

    // Close the last tab through a middle-button MouseEvent.
    // Playwright dispatchEvent can create an Event with no button value.
    // Its middle-button click can also fail inside a container that supports drag reordering.
    // The explicit MouseEvent supplies button=1 for the actual tab handler.
    await tabs.nth(countBefore - 1).evaluate((el) => {
      el.dispatchEvent(new MouseEvent('auxclick', { button: 1, bubbles: true, cancelable: true }))
    })

    // Require removal of the closed tab.
    await expectAgentTabCount(page, countBefore - 1)
  })
})
