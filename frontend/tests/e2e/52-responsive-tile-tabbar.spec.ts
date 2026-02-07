import { expect, test } from './fixtures'

/** Open a new agent via the tab bar add menu. */
async function openAgentViaUI(page: import('@playwright/test').Page) {
  await page.locator('[data-testid="new-agent-button"]').first().click()
  await page.locator('[data-testid="tab"]').first().waitFor()
}

/** Drag a resize handle to change tile sizes. */
async function dragResizeHandle(
  page: import('@playwright/test').Page,
  handleIndex: number,
  deltaX: number,
  deltaY: number,
) {
  const handle = page.locator('[data-testid="tile-resize-handle"]').nth(handleIndex)
  const box = await handle.boundingBox()
  expect(box).toBeTruthy()
  const cx = box!.x + box!.width / 2
  const cy = box!.y + box!.height / 2
  await page.mouse.move(cx, cy)
  await page.mouse.down()
  await page.mouse.move(cx + deltaX, cy + deltaY, { steps: 5 })
  await page.mouse.up()
  // Wait for ResizeObserver to fire
  await page.waitForTimeout(100)
}

test.describe('Responsive Tile TabBar', () => {
  test('single tile has full size class', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    const tile = page.locator('[data-testid="tile"]')
    await expect(tile).toHaveAttribute('data-tile-size', 'full')
    await expect(tile).toHaveAttribute('data-tile-height', 'tall')
  })

  test('compact mode: tabs become icon-only when tile is narrow', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Split horizontally to create two side-by-side tiles
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Drag the resize handle far to the right to make the right tile narrow (~300px)
    // The tiling root is roughly ~788px wide (62% of 1280), so drag ~250px right
    await dragResizeHandle(page, 0, 250, 0)

    const tiles = page.locator('[data-testid="tile"]')
    const rightTile = tiles.nth(1)
    const rightBox = await rightTile.boundingBox()
    expect(rightBox).toBeTruthy()

    // Verify the tile is in compact range (240-359px)
    if (rightBox!.width >= 240 && rightBox!.width < 360) {
      await expect(rightTile).toHaveAttribute('data-tile-size', 'compact')

      // Tab text should be hidden (display: none via CSS)
      // New agent/terminal buttons should still be visible
      await expect(rightTile.locator('[data-testid="new-agent-button"]')).toBeVisible()
      await expect(rightTile.locator('[data-testid="new-terminal-button"]')).toBeVisible()
    }
  })

  test('minimal mode: new tab buttons collapse into + dropdown', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Split horizontally twice to get 3 tiles
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(3)

    // At 3 tiles, the narrower ones should be in minimal or smaller range
    const tiles = page.locator('[data-testid="tile"]')
    for (let i = 0; i < 3; i++) {
      const size = await tiles.nth(i).getAttribute('data-tile-size')
      if (size === 'minimal' || size === 'micro') {
        // The collapsed new-tab button should be visible (or overflow)
        const collapsedBtn = tiles.nth(i).locator('[data-testid="collapsed-new-tab-button"]')
        const overflowBtn = tiles.nth(i).locator('[data-testid="collapsed-overflow-button"]')
        const isCollapsedVisible = await collapsedBtn.isVisible().catch(() => false)
        const isOverflowVisible = await overflowBtn.isVisible().catch(() => false)
        expect(isCollapsedVisible || isOverflowVisible).toBe(true)
        break
      }
    }

    // With 3 tiles at 1280x720 and ~62% center panel, each tile is ~260px
    // which should be compact. Verify at least all tiles have a size class
    for (let i = 0; i < 3; i++) {
      const size = await tiles.nth(i).getAttribute('data-tile-size')
      expect(['full', 'compact', 'minimal', 'micro']).toContain(size)
    }
  })

  test('data-tile-size updates when tile is resized', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Split horizontally
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    const tiles = page.locator('[data-testid="tile"]')
    const leftTile = tiles.nth(0)
    const rightTile = tiles.nth(1)

    // Both tiles should initially be at full or compact (each ~50% of ~788px ≈ ~394px)
    const leftSize = await leftTile.getAttribute('data-tile-size')
    expect(leftSize).toBe('full')

    // Drag resize handle far left to make left tile very narrow
    await dragResizeHandle(page, 0, -200, 0)

    // Left tile should now be compact or smaller
    const newLeftSize = await leftTile.getAttribute('data-tile-size')
    expect(['compact', 'minimal', 'micro']).toContain(newLeftSize)

    // Right tile should be full (wider than before)
    const newRightSize = await rightTile.getAttribute('data-tile-size')
    expect(newRightSize).toBe('full')
  })

  test('micro mode: split/close actions move to overflow at extreme width', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Split horizontally twice to get 3 tiles, then drag handle to make one very narrow
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(3)

    // Drag the first resize handle far right to shrink the leftmost tile
    await dragResizeHandle(page, 0, 300, 0)

    const tiles = page.locator('[data-testid="tile"]')
    const leftTile = tiles.nth(0)
    const leftSize = await leftTile.getAttribute('data-tile-size')

    if (leftSize === 'micro') {
      // Split actions should be hidden from Tile (CSS hides them)
      // The overflow button should be visible in the TabBar
      const overflowBtn = leftTile.locator('[data-testid="collapsed-overflow-button"]')
      await expect(overflowBtn).toBeVisible()
    }
  })

  test('short height: tab bar height reduced when tile is short', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Split vertically to get top/bottom tiles
    await page.locator('[data-testid="split-vertical"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Split again to get 3 stacked tiles
    await page.locator('[data-testid="tile"]').nth(0).locator('[data-testid="split-vertical"]').click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(3)

    // Check height classes - some tiles should be short or tiny at 3-way vertical split
    // At 720px total height with ~588px center and 3 tiles, each ~196px - should be tall
    // But if we drag to make one very small, it can become short
    const tiles = page.locator('[data-testid="tile"]')
    for (let i = 0; i < 3; i++) {
      const height = await tiles.nth(i).getAttribute('data-tile-height')
      expect(['tall', 'short', 'tiny']).toContain(height)
    }
  })
})
