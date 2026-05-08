import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { openAgentViaUI } from './helpers/ui'

/**
 * Grid layout end-to-end tests. Each test starts from an authenticated
 * workspace with a single tile and exercises the click-driven flows the
 * unit tests can't cover (real DOM, real CSS Grid layout, real persistence
 * round-trip).
 *
 * Note: the authenticatedWorkspace fixture seeds an initial agent tab. Tests
 * that need a no-tab tile close that tab via the tab-close button first.
 */

async function makeGrid(page: Page, rows: number, cols: number) {
  await page.locator('[data-testid="make-grid"]').first().click()
  await expect(page.locator('[data-testid="grid-size-popover"]')).toBeVisible()
  await page.locator(`[data-testid="grid-size-cell-${rows - 1}-${cols - 1}"]`).click()
  await expect(page.locator('[data-testid="tile-grid"]')).toBeVisible()
}

test.describe('Grid layout', () => {
  test('Make a grid creates a 2×2 with close button only on top-right cell', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await makeGrid(page, 2, 2)

    const tiles = page.locator('[data-testid="tile"]')
    await expect(tiles).toHaveCount(4)

    const topRightCell = page.locator('[data-grid-row="0"][data-grid-col="1"]')
    await expect(topRightCell.locator('[data-testid="close-grid"]')).toBeVisible()

    await expect(page.locator('[data-testid="close-tile"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="close-grid"]')).toHaveCount(1)
  })

  test('Convert to tile preserves the agent tab on the merged tile', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    // The fixture pre-seeds an agent tab; capture the count and use it as the
    // baseline so we don't depend on it being exactly 1.
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    const baseline = await agentTabs.count()
    await openAgentViaUI(page)
    await expect(agentTabs).toHaveCount(baseline + 1)

    await makeGrid(page, 2, 2)

    await page.locator('[data-testid="close-grid"]').click()
    await expect(page.locator('[data-testid="close-grid-dialog"]')).toBeVisible()
    await page.locator('[data-testid="close-grid-convert"]').click()

    // Grid is gone, exactly one tile, agent tabs preserved.
    await expect(page.locator('[data-testid="tile-grid"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(1)
    await expect(agentTabs).toHaveCount(baseline + 1)
  })

  test('Make-grid popover Create button is disabled for out-of-range manual values', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await page.locator('[data-testid="make-grid"]').first().click()
    await expect(page.locator('[data-testid="grid-size-popover"]')).toBeVisible()
    const create = page.locator('[data-testid="grid-size-create-button"]')
    await expect(create).toBeDisabled()
    await page.locator('[data-testid="grid-size-rows-input"]').fill('21')
    await page.locator('[data-testid="grid-size-cols-input"]').fill('1')
    await expect(create).toBeDisabled()
    await page.locator('[data-testid="grid-size-rows-input"]').fill('8')
    await page.locator('[data-testid="grid-size-cols-input"]').fill('5')
    await expect(create).not.toBeDisabled()
    await create.click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(40)
  })

  // Drag-resize and no-tab close-grid paths are covered by the unit tests
  // (TilingLayout/GridRenderer setDragColRatios behaviour, layout.store
  // removeGrid for the empty-grid case). The corresponding e2e flows depend
  // on Playwright pointer-event synthesis for the column drag and on the
  // worktree-confirmation dialog for closing the workspace's seeded agent
  // tab, both of which are environment-fragile in CI.
})
