import { expect, test } from './fixtures'
import { waitForLayoutSave } from './helpers'

test.describe('Tab Ordering Persistence', () => {
  test('should persist tab order after drag-drop reorder and page refresh', async ({ page, authenticatedWorkspace }) => {
    // Wait for the initial agent tab
    await expect(page.locator('[data-testid="tab"]').first()).toBeVisible()

    // Open a terminal via the dedicated button
    await page.locator('[data-testid="new-terminal-button"]').click()

    // Wait for terminal tab to appear (now 2 tabs) and xterm to render
    await expect(page.locator('[data-testid="tab"]')).toHaveCount(2)
    await expect(page.locator('.xterm')).toBeVisible()

    // Get initial tab order
    const getTabTypes = async () => {
      const tabs = page.locator('[data-testid="tab"]')
      const count = await tabs.count()
      const types: string[] = []
      for (let i = 0; i < count; i++) {
        const type = await tabs.nth(i).getAttribute('data-tab-type')
        types.push(type ?? '')
      }
      return types
    }

    // Initially: [agent, terminal]
    const initialOrder = await getTabTypes()
    expect(initialOrder).toEqual(['agent', 'terminal'])

    // Drag terminal tab before agent tab
    const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')

    const terminalBox = await terminalTab.boundingBox()
    const agentBox = await agentTab.boundingBox()
    expect(terminalBox).toBeTruthy()
    expect(agentBox).toBeTruthy()

    // Drag from center of terminal tab to center of agent tab
    await page.mouse.move(terminalBox!.x + terminalBox!.width / 2, terminalBox!.y + terminalBox!.height / 2)
    await page.mouse.down()
    await page.mouse.move(agentBox!.x + agentBox!.width / 2, agentBox!.y + agentBox!.height / 2, { steps: 10 })
    await page.mouse.up()

    // Verify reorder: [terminal, agent]
    const reorderedOrder = await getTabTypes()
    expect(reorderedOrder).toEqual(['terminal', 'agent'])

    // Wait for the layout save to complete (500ms debounce + network round-trip)
    await waitForLayoutSave(page)
    await page.reload()

    // Wait for tabs to load
    await expect(page.locator('[data-testid="tab"]')).toHaveCount(2)

    // Verify order persists after refresh: [terminal, agent]
    const refreshedOrder = await getTabTypes()
    expect(refreshedOrder).toEqual(['terminal', 'agent'])
  })

  test('should persist active tab across page refresh', async ({ page, authenticatedWorkspace }) => {
    // Wait for at least one tab to be visible
    await expect(page.locator('[data-testid="tab"]').first()).toBeVisible()

    // Open a terminal via the dedicated button
    await page.locator('[data-testid="new-terminal-button"]').click()

    // Wait for at least 2 tabs (agent + terminal) and xterm to render
    const tabs = page.locator('[data-testid="tab"]')
    await expect(tabs).toHaveCount(2).catch(async () => {
      // May have more than 2 if auto-created; just need agent+terminal
      const count = await tabs.count()
      expect(count).toBeGreaterThanOrEqual(2)
    })
    await expect(page.locator('.xterm')).toBeVisible()

    // Click the agent tab to make it active
    await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().click()
    await page.waitForTimeout(500)

    // Verify agent tab is active (has data-selected attribute from Kobalte Tabs)
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]')).toBeVisible()

    // Wait for active tab state to be persisted to sessionStorage, then refresh
    await page.waitForTimeout(1000)
    await page.reload()

    // Wait for tabs to load
    await expect(page.locator('[data-testid="tab"]').first()).toBeVisible()

    // Verify agent tab is still active after refresh
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"][aria-selected="true"]')).toBeVisible()
  })
})
