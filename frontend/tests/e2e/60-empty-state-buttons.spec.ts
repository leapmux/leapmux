import { expect, test } from './fixtures'
import { loginViaToken } from './helpers'

/** Close all tabs in the workspace by clicking close buttons. */
async function closeAllTabs(page: import('@playwright/test').Page) {
  const tabs = page.locator('[data-testid="tab"]')
  const count = await tabs.count()
  for (let i = count - 1; i >= 0; i--) {
    const closeBtn = tabs.nth(i).locator('[data-testid="tab-close"]')
    if (await closeBtn.isVisible()) {
      await closeBtn.click()
      await page.waitForTimeout(500)
    }
  }
  await expect(page.locator('[data-testid="tab"]')).toHaveCount(0)
}

test.describe('Empty State Buttons', () => {
  test('empty tile shows agent and terminal buttons', async ({ page, authenticatedWorkspace }) => {
    await closeAllTabs(page)

    const actions = page.locator('[data-testid="empty-tile-actions"]')
    await expect(actions).toBeVisible()
    await expect(page.locator('[data-testid="empty-tile-open-agent"]')).toBeVisible()
    await expect(page.locator('[data-testid="empty-tile-open-terminal"]')).toBeVisible()
    await expect(page.locator('[data-testid="empty-tile-open-agent"]')).toHaveText(/Open a new agent tab/)
    await expect(page.locator('[data-testid="empty-tile-open-terminal"]')).toHaveText(/Open a new terminal tab/)
  })

  test('clicking agent button opens agent or dialog', async ({ page, authenticatedWorkspace }) => {
    await closeAllTabs(page)
    await expect(page.locator('[data-testid="empty-tile-actions"]')).toBeVisible()

    await page.locator('[data-testid="empty-tile-open-agent"]').click()

    // Should open either a new agent tab or the new agent dialog
    await expect(
      page.locator('[data-testid="tab"][data-tab-type="agent"]')
        .or(page.getByRole('heading', { name: 'New Agent' })),
    ).toBeVisible({ timeout: 10_000 })
  })

  test('clicking terminal button opens terminal or dialog', async ({ page, authenticatedWorkspace }) => {
    await closeAllTabs(page)
    await expect(page.locator('[data-testid="empty-tile-actions"]')).toBeVisible()

    await page.locator('[data-testid="empty-tile-open-terminal"]').click()

    // Should open either a new terminal tab or the new terminal dialog
    await expect(
      page.locator('[data-testid="tab"][data-tab-type="terminal"]')
        .or(page.getByRole('heading', { name: 'New Terminal' })),
    ).toBeVisible({ timeout: 10_000 })
  })

  test('multi-tile: unfocused empty tile shows hint, focused shows buttons', async ({ page, authenticatedWorkspace }) => {
    // Start with a tab, then split to create a second tile
    await page.locator('[data-testid="new-agent-button"]').click()
    await page.locator('[data-testid="tab"]').first().waitFor()
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // The second tile is empty — it should show either hint or actions depending on focus.
    // Click on the first tile to ensure it's focused.
    const tile1 = page.locator('[data-testid="tile"]').nth(0)
    await tile1.click()
    await page.waitForTimeout(300)

    // The unfocused empty tile should show the hint text
    const tile2 = page.locator('[data-testid="tile"]').nth(1)
    await expect(tile2.locator('[data-testid="empty-tile-hint"]')).toBeVisible()

    // Now click on the second tile to focus it
    await tile2.click()
    await page.waitForTimeout(300)

    // Focused empty tile should show action buttons
    await expect(tile2.locator('[data-testid="empty-tile-actions"]')).toBeVisible()
    await expect(tile2.locator('[data-testid="empty-tile-open-agent"]')).toBeVisible()
    await expect(tile2.locator('[data-testid="empty-tile-open-terminal"]')).toBeVisible()
  })

  test('no-workspace state shows create workspace button', async ({ page, leapmuxServer }) => {
    const { adminToken } = leapmuxServer

    // Log in and navigate to org root — other tests may have created workspaces
    // which triggers auto-redirect. Only assert if the empty state is reachable.
    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')

    // Wait for page to settle
    const createBtn = page.locator('[data-testid="create-workspace-button"]')
    const sectionHeader = page.locator('[data-testid="section-header-workspaces_in_progress"]')
      .or(page.locator('[data-testid="section-header-workspaces_archived"]'))

    await expect(createBtn.or(sectionHeader)).toBeVisible()

    // Only test the button behavior if the empty state is visible
    if (await createBtn.isVisible()) {
      await expect(createBtn).toHaveText(/Create a new workspace/)
      await createBtn.click()
      await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeVisible()
      await page.keyboard.press('Escape')
    }
  })
})
