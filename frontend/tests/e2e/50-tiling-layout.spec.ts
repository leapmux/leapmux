import { expect, test } from './fixtures'
import { waitForLayoutSave } from './helpers'

/** Open a new agent via the tab bar add menu. */
async function openAgentViaUI(page: import('@playwright/test').Page) {
  await page.locator('[data-testid="new-agent-button"]').click()
  // Wait for a new tab to appear
  await page.locator('[data-testid="tab"]').first().waitFor()
}

/** Get the bounding box of the tiling layout's resizable root (not the AppShell outer layout). */
async function getTilingRootBox(page: import('@playwright/test').Page) {
  return page.evaluate(() => {
    const tile = document.querySelector('[data-testid="tile"]')
    if (!tile)
      return null
    let el = tile.parentElement
    while (el && !el.hasAttribute('data-corvu-resizable-root')) {
      el = el.parentElement
    }
    if (!el)
      return null
    const r = el.getBoundingClientRect()
    return { x: r.x, y: r.y, width: r.width, height: r.height }
  })
}

test.describe('Tiling Layout', () => {
  test('default layout is single tile', async ({ page, authenticatedWorkspace }) => {
    // Should have exactly one tile with no resize handles between tiles
    const tiles = page.locator('[data-testid="tile"]')
    await expect(tiles).toHaveCount(1)

    // No tile resize handles visible
    const resizeHandles = page.locator('[data-testid="tile-resize-handle"]')
    await expect(resizeHandles).toHaveCount(0)
  })

  test('split horizontal creates two panes side-by-side', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Click the horizontal split button on the tile
    await page.locator('[data-testid="split-horizontal"]').first().click()

    // Should now have 2 tiles
    const tiles = page.locator('[data-testid="tile"]')
    await expect(tiles).toHaveCount(2)

    // Should have at least one resize handle between them
    const resizeHandles = page.locator('[data-testid="tile-resize-handle"]')
    await expect(resizeHandles).toHaveCount(1)
  })

  test('split vertical creates two panes stacked', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Click the vertical split button
    await page.locator('[data-testid="split-vertical"]').first().click()

    // Should now have 2 tiles
    const tiles = page.locator('[data-testid="tile"]')
    await expect(tiles).toHaveCount(2)

    // Should have a resize handle
    const resizeHandles = page.locator('[data-testid="tile-resize-handle"]')
    await expect(resizeHandles).toHaveCount(1)
  })

  test('close tile collapses layout back to single tile', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Split into 2 tiles
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Close the second tile
    const closeTileButtons = page.locator('[data-testid="close-tile"]')
    await closeTileButtons.last().click()

    // Should be back to 1 tile
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(1)

    // No resize handles
    await expect(page.locator('[data-testid="tile-resize-handle"]')).toHaveCount(0)
  })

  test('split buttons show only when multiple tiles exist', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Close tile button should not be visible with single tile
    await expect(page.locator('[data-testid="close-tile"]')).toHaveCount(0)

    // Split into 2 tiles
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Close tile buttons should now be visible
    await expect(page.locator('[data-testid="close-tile"]')).toHaveCount(2)
  })

  test('layout persists across page reload', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Split into 2 tiles
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Wait for the layout save to complete (500ms debounce + network round-trip)
    await waitForLayoutSave(page)

    // Reload
    await page.reload()

    // Wait for workspace to load
    await page.locator('[data-testid="tile"]').first().waitFor({ timeout: 10000 })

    // Should still have 2 tiles after reload
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)
  })

  test('each tile has its own tab bar', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Split into 2 tiles
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Each tile should have a tab bar (containing tab list)
    const tabBars = page.locator('[data-testid="tab-bar"]')
    await expect(tabBars).toHaveCount(2)
  })

  test('split same direction twice creates 3 tiles with correct layout', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Split horizontally twice on the first tile
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(3)

    // All 3 tiles should have positive width (no collapsed/zero-width tiles)
    const tiles = page.locator('[data-testid="tile"]')
    for (let i = 0; i < 3; i++) {
      const box = await tiles.nth(i).boundingBox()
      expect(box).toBeTruthy()
      expect(box!.width).toBeGreaterThan(50)
    }

    // Should have 2 resize handles
    await expect(page.locator('[data-testid="tile-resize-handle"]')).toHaveCount(2)
  })

  test('split same direction twice then close rightmost tile leaves no gap', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Split horizontally twice
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(3)

    // Close the rightmost tile
    await page.locator('[data-testid="close-tile"]').last().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Both remaining tiles should have positive width and fill the available space
    const tiles = page.locator('[data-testid="tile"]')
    const box0 = await tiles.nth(0).boundingBox()
    const box1 = await tiles.nth(1).boundingBox()
    expect(box0).toBeTruthy()
    expect(box1).toBeTruthy()
    expect(box0!.width).toBeGreaterThan(50)
    expect(box1!.width).toBeGreaterThan(50)

    // The tiles should approximately fill their tiling layout parent (no large gap).
    const parentBox = await getTilingRootBox(page)
    if (parentBox) {
      const rightEdge = box1!.x + box1!.width
      const parentRightEdge = parentBox.x + parentBox.width
      expect(Math.abs(rightEdge - parentRightEdge)).toBeLessThan(10)
    }
  })

  test('split same direction twice then close middle tile preserves remaining tiles', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Split horizontally twice
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(3)

    // Close the middle tile (index 1)
    await page.locator('[data-testid="close-tile"]').nth(1).click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Both remaining tiles should have positive width
    const tiles = page.locator('[data-testid="tile"]')
    const box0 = await tiles.nth(0).boundingBox()
    const box1 = await tiles.nth(1).boundingBox()
    expect(box0).toBeTruthy()
    expect(box1).toBeTruthy()
    expect(box0!.width).toBeGreaterThan(50)
    expect(box1!.width).toBeGreaterThan(50)
  })

  test('mixed-direction split then sequential close restores layout', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    const tiles = page.locator('[data-testid="tile"]')
    const closeTileButtons = page.locator('[data-testid="close-tile"]')

    // Step 1: Split horizontally to get Tile1 (left) | Tile2 (right)
    // Note: "split-horizontal" = horizontal orientation = panels side by side
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(tiles).toHaveCount(2)
    await expect(page.locator('[data-testid="tile-resize-handle"]')).toHaveCount(1)

    // Verify the two tiles are side by side
    const box2_0 = await tiles.nth(0).boundingBox()
    const box2_1 = await tiles.nth(1).boundingBox()
    expect(box2_0).toBeTruthy()
    expect(box2_1).toBeTruthy()
    // Tile2 should be to the right of Tile1
    expect(box2_1!.x).toBeGreaterThan(box2_0!.x)
    // Both should have approximately half the tiling area width
    const tilingBox = await getTilingRootBox(page)
    expect(tilingBox).toBeTruthy()
    const halfWidth = tilingBox!.width / 2
    expect(box2_0!.width).toBeGreaterThan(halfWidth * 0.8)
    expect(box2_0!.width).toBeLessThan(halfWidth * 1.2)

    // Step 2: Split the right tile (Tile2) vertically to get:
    //          | Tile2
    //   Tile1  +--------
    //          | Tile3
    // Note: "split-vertical" = vertical orientation = panels stacked top/bottom
    // Target the split button inside the second tile specifically
    const splitBtn = tiles.nth(1).locator('[data-testid="split-vertical"]')
    await expect(splitBtn).toBeVisible()
    await splitBtn.click()
    await expect(tiles).toHaveCount(3)
    // 2 resize handles: one between Tile1 and right side,
    // one between Tile2 and Tile3
    await expect(page.locator('[data-testid="tile-resize-handle"]')).toHaveCount(2)

    // Verify 3-tile layout:
    // Tile1 (left half), Tile2 (top-right quarter), Tile3 (bottom-right quarter)
    const box3_0 = await tiles.nth(0).boundingBox()
    const box3_1 = await tiles.nth(1).boundingBox()
    const box3_2 = await tiles.nth(2).boundingBox()
    expect(box3_0).toBeTruthy()
    expect(box3_1).toBeTruthy()
    expect(box3_2).toBeTruthy()
    // Tile1 should be on the left
    expect(box3_0!.x).toBeLessThan(box3_1!.x)
    expect(box3_0!.x).toBeLessThan(box3_2!.x)
    // Tile2 and Tile3 should be stacked vertically on the right
    expect(Math.abs(box3_1!.x - box3_2!.x)).toBeLessThan(10)
    expect(box3_1!.y).toBeLessThan(box3_2!.y)
    // Tile1 should span the full height
    expect(box3_0!.height).toBeGreaterThan(box3_1!.height + box3_2!.height - 20)

    // Step 3: Close Tile2 (top-right) to get Tile1 | Tile3
    // Tile2 is the second tile (index 1)
    await closeTileButtons.nth(1).click()
    await expect(tiles).toHaveCount(2)
    await expect(page.locator('[data-testid="tile-resize-handle"]')).toHaveCount(1)

    // Verify 2-tile layout: both tiles side by side, filling the area
    const box4_0 = await tiles.nth(0).boundingBox()
    const box4_1 = await tiles.nth(1).boundingBox()
    expect(box4_0).toBeTruthy()
    expect(box4_1).toBeTruthy()
    expect(box4_0!.width).toBeGreaterThan(50)
    expect(box4_1!.width).toBeGreaterThan(50)
    // Both tiles should have the full height (no vertical split remains)
    const tilingBox2 = await getTilingRootBox(page)
    expect(tilingBox2).toBeTruthy()
    expect(box4_0!.height).toBeGreaterThan(tilingBox2!.height * 0.9)
    expect(box4_1!.height).toBeGreaterThan(tilingBox2!.height * 0.9)
    // Tiles should fill the width (no gap)
    const rightEdge = box4_1!.x + box4_1!.width
    const parentRightEdge = tilingBox2!.x + tilingBox2!.width
    expect(Math.abs(rightEdge - parentRightEdge)).toBeLessThan(10)

    // Step 4: Close Tile3 to go back to single tile
    await closeTileButtons.last().click()
    await expect(tiles).toHaveCount(1)
    await expect(page.locator('[data-testid="tile-resize-handle"]')).toHaveCount(0)
    await expect(closeTileButtons).toHaveCount(0)
  })

  test('nested split creates correct 4-tile layout', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    const tiles = page.locator('[data-testid="tile"]')

    // Step 1: Split horizontally → Tile1 | Tile2
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(tiles).toHaveCount(2)

    // Step 2: Split Tile2 (right) vertically → Tile1 | [Tile2 / Tile3]
    await tiles.nth(1).locator('[data-testid="split-vertical"]').click()
    await expect(tiles).toHaveCount(3)

    // Step 3: Split Tile3 (bottom-right) vertically → Tile1 | [Tile2 / Tile3 / Tile4]
    // Tile3 is at index 2; splitting it in the same vertical direction
    // should add Tile4 as a sibling (not nest deeper).
    await tiles.nth(2).locator('[data-testid="split-vertical"]').click()
    await expect(tiles).toHaveCount(4)

    // Verify 4-tile layout:
    //          | Tile2
    //          +--------
    //   Tile1  | Tile3
    //          +--------
    //          | Tile4
    const box0 = await tiles.nth(0).boundingBox()
    const box1 = await tiles.nth(1).boundingBox()
    const box2 = await tiles.nth(2).boundingBox()
    const box3 = await tiles.nth(3).boundingBox()
    expect(box0).toBeTruthy()
    expect(box1).toBeTruthy()
    expect(box2).toBeTruthy()
    expect(box3).toBeTruthy()

    // Tile1 should be on the left
    expect(box0!.x).toBeLessThan(box1!.x)
    expect(box0!.x).toBeLessThan(box2!.x)
    expect(box0!.x).toBeLessThan(box3!.x)

    // Tiles 2, 3, 4 should be stacked vertically on the right at the same x
    expect(Math.abs(box1!.x - box2!.x)).toBeLessThan(10)
    expect(Math.abs(box2!.x - box3!.x)).toBeLessThan(10)
    expect(box1!.y).toBeLessThan(box2!.y)
    expect(box2!.y).toBeLessThan(box3!.y)

    // All tiles should have positive dimensions
    expect(box0!.width).toBeGreaterThan(50)
    expect(box1!.width).toBeGreaterThan(50)
    expect(box2!.width).toBeGreaterThan(50)
    expect(box3!.width).toBeGreaterThan(50)
    expect(box0!.height).toBeGreaterThan(50)
    expect(box1!.height).toBeGreaterThan(50)
    expect(box2!.height).toBeGreaterThan(50)
    expect(box3!.height).toBeGreaterThan(50)

    // Tile1 should span the full height
    const tilingBox = await getTilingRootBox(page)
    expect(tilingBox).toBeTruthy()
    expect(box0!.height).toBeGreaterThan(tilingBox!.height * 0.9)

    // 3 resize handles total: 1 horizontal (between left/right) + 2 vertical (between stacked tiles)
    await expect(page.locator('[data-testid="tile-resize-handle"]')).toHaveCount(3)
  })
})
