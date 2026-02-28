import type { Locator, Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { waitForLayoutSave } from './helpers/ui'

/** Wait for the workspace to be fully loaded with its initial agent tab. */
async function waitForInitialAgent(page: Page) {
  await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().waitFor({ timeout: 10000 })
}

/** Open a new terminal in a specific tile. */
async function openTerminalInTile(page: Page, tile: Locator) {
  await tile.locator('[data-testid="new-terminal-button"]').click()
  await tile.locator('[data-testid="tab"][data-tab-type="terminal"]').first().waitFor()
}

/** Open a new terminal using the first available button (for single-tile). */
async function openTerminal(page: Page) {
  await page.locator('[data-testid="new-terminal-button"]').first().click()
  await page.locator('[data-testid="tab"][data-tab-type="terminal"]').first().waitFor()
}

/** Split the first tile horizontally (side-by-side). */
async function splitHorizontal(page: Page, tile?: Locator) {
  const target = tile ?? page.locator('[data-testid="split-horizontal"]').first()
  if (tile) {
    await tile.locator('[data-testid="split-horizontal"]').click()
  }
  else {
    await target.click()
  }
}

/** Simulate a drag-and-drop from one element to another using mouse events. */
async function dragTo(page: Page, source: Locator, target: Locator) {
  const sourceBox = await source.boundingBox()
  const targetBox = await target.boundingBox()
  if (!sourceBox || !targetBox)
    throw new Error('Could not get bounding boxes for drag operation')

  const srcX = sourceBox.x + sourceBox.width / 2
  const srcY = sourceBox.y + sourceBox.height / 2
  const tgtX = targetBox.x + targetBox.width / 2
  const tgtY = targetBox.y + targetBox.height / 2

  await page.mouse.move(srcX, srcY)
  await page.mouse.down()
  // Move in steps to trigger DnD sensors
  const steps = 5
  for (let i = 1; i <= steps; i++) {
    await page.mouse.move(
      srcX + (tgtX - srcX) * (i / steps),
      srcY + (tgtY - srcY) * (i / steps),
      { steps: 1 },
    )
    await page.waitForTimeout(30)
  }
  await page.mouse.up()
}

/** Wait for the debounced layout save to complete. */
async function waitForLayoutPersistence(page: Page) {
  // The debounce timer is 500ms; wait a bit more for the network round-trip.
  await page.waitForTimeout(1500)
}

test.describe('Cross-Tile Tabs', () => {
  test('close tile cleans up its tabs', async ({ page, authenticatedWorkspace }) => {
    // Wait for the auto-created initial agent tab
    await waitForInitialAgent(page)
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)

    // Split horizontally to get 2 tiles
    await splitHorizontal(page)
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Open terminal in the second tile
    const tile2 = page.locator('[data-testid="tile"]').nth(1)
    await openTerminalInTile(page, tile2)

    // Verify we have an agent tab and a terminal tab
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toHaveCount(1)

    // Close the second tile
    await tile2.locator('[data-testid="close-tile"]').click()

    // Should be back to 1 tile
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(1)

    // Wait for cleanup to happen
    await waitForLayoutPersistence(page)

    // The terminal tab should be cleaned up (hub-side cleanup on tile close)
    // Only agent tab should remain
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toHaveCount(0)
  })

  test('split and create tabs in each tile, close tile - cleanup works', async ({ page, authenticatedWorkspace }) => {
    // Wait for auto-created initial agent tab
    await waitForInitialAgent(page)

    // Split horizontally
    await splitHorizontal(page)
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Open terminal in second tile
    const tile2 = page.locator('[data-testid="tile"]').nth(1)
    await openTerminalInTile(page, tile2)
    await expect(page.locator('[data-testid="tab"]')).toHaveCount(2)

    // Close tile 2
    await tile2.locator('[data-testid="close-tile"]').click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(1)

    await waitForLayoutPersistence(page)

    // Terminal tab should be removed, agent tab remains
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toHaveCount(0)
  })

  test('each tile gets independent tabs after split', async ({ page, authenticatedWorkspace }) => {
    // Wait for auto-created initial agent tab
    await waitForInitialAgent(page)

    // Split horizontally
    await splitHorizontal(page)
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Open terminal in tile 2
    const tile2 = page.locator('[data-testid="tile"]').nth(1)
    await openTerminalInTile(page, tile2)

    // Tile 1 should have the agent tab
    const tile1 = page.locator('[data-testid="tile"]').nth(0)
    await expect(tile1.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)

    // Tile 2 should have the terminal tab
    await expect(tile2.locator('[data-testid="tab"][data-tab-type="terminal"]')).toHaveCount(1)
  })

  test('cross-tile tab drag moves tab to target tile', async ({ page, authenticatedWorkspace }) => {
    // Wait for auto-created initial agent tab
    await waitForInitialAgent(page)

    // Also create a terminal in the same tile
    await openTerminal(page)

    // Split horizontally to get 2 tiles
    await splitHorizontal(page)
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Tile 1 should have both tabs (agent + terminal)
    const tile1 = page.locator('[data-testid="tile"]').nth(0)
    const tile2 = page.locator('[data-testid="tile"]').nth(1)
    await expect(tile1.locator('[data-testid="tab"]')).toHaveCount(2)

    // Drag terminal tab from tile 1 to tile 2's tab bar
    const terminalTab = tile1.locator('[data-testid="tab"][data-tab-type="terminal"]')
    const tile2TabList = tile2.locator('[data-testid="tab-list"]')
    await dragTo(page, terminalTab, tile2TabList)

    // Wait for the move to settle
    await page.waitForTimeout(500)

    // Tile 1 should have 1 tab (agent)
    await expect(tile1.locator('[data-testid="tab"]')).toHaveCount(1)
    await expect(tile1.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)

    // Tile 2 should have 1 tab (terminal)
    await expect(tile2.locator('[data-testid="tab"]')).toHaveCount(1)
    await expect(tile2.locator('[data-testid="tab"][data-tab-type="terminal"]')).toHaveCount(1)
  })

  test('cross-tile drag persists after reload', async ({ page, authenticatedWorkspace }) => {
    // Wait for auto-created initial agent
    await waitForInitialAgent(page)

    // Create terminal and drain its layout save so it doesn't interfere later
    const terminalSaved = waitForLayoutSave(page)
    await openTerminal(page)
    await terminalSaved

    // Split and drain its layout save
    const splitSaved = waitForLayoutSave(page)
    await splitHorizontal(page)
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)
    await splitSaved

    const tile1 = page.locator('[data-testid="tile"]').nth(0)
    const tile2 = page.locator('[data-testid="tile"]').nth(1)

    // Set up listener for the cross-tile move's save before the drag
    const dragSaved = waitForLayoutSave(page)

    // Drag terminal tab from tile 1 to tile 2
    const terminalTab = tile1.locator('[data-testid="tab"][data-tab-type="terminal"]')
    const tile2TabList = tile2.locator('[data-testid="tab-list"]')
    await dragTo(page, terminalTab, tile2TabList)
    await page.waitForTimeout(500)

    // Verify move happened
    await expect(tile1.locator('[data-testid="tab"]')).toHaveCount(1)
    await expect(tile2.locator('[data-testid="tab"]')).toHaveCount(1)

    // Wait for the drag's layout save to complete
    await dragSaved

    // Reload
    await page.reload()
    await page.locator('[data-testid="tile"]').first().waitFor({ timeout: 10000 })

    // Should still have 2 tiles
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Terminal should still be in tile 2 after reload
    const tile1After = page.locator('[data-testid="tile"]').nth(0)
    const tile2After = page.locator('[data-testid="tile"]').nth(1)
    await expect(tile1After.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    await expect(tile2After.locator('[data-testid="tab"][data-tab-type="terminal"]')).toHaveCount(1)
  })

  test('split layout: create 4 tiles via sequential splits', async ({ page, authenticatedWorkspace }) => {
    await waitForInitialAgent(page)

    // Split horizontally to get 2 tiles
    await splitHorizontal(page)
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Split each tile vertically to get 4 tiles
    const tile1 = page.locator('[data-testid="tile"]').nth(0)
    await tile1.locator('[data-testid="split-vertical"]').click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(3)

    const tile2 = page.locator('[data-testid="tile"]').nth(1)
    await tile2.locator('[data-testid="split-vertical"]').dispatchEvent('click')
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(4)

    // Open terminal in different tiles
    const tileB = page.locator('[data-testid="tile"]').nth(1)
    const tileC = page.locator('[data-testid="tile"]').nth(2)
    await openTerminalInTile(page, tileB)
    await openTerminalInTile(page, tileC)

    // Verify: tile 0 has agent, tile 1 has terminal, tile 2 has terminal
    const tileA = page.locator('[data-testid="tile"]').nth(0)
    await expect(tileA.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    await expect(tileB.locator('[data-testid="tab"][data-tab-type="terminal"]')).toHaveCount(1)
    await expect(tileC.locator('[data-testid="tab"][data-tab-type="terminal"]')).toHaveCount(1)
  })

  test('resize handle changes tile sizes', async ({ page, authenticatedWorkspace }) => {
    await waitForInitialAgent(page)

    // Split horizontally to get 2 tiles
    await splitHorizontal(page)
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Get initial bounding boxes
    const tiles = page.locator('[data-testid="tile"]')
    const box1Before = await tiles.nth(0).boundingBox()
    const box2Before = await tiles.nth(1).boundingBox()
    expect(box1Before).toBeTruthy()
    expect(box2Before).toBeTruthy()

    // They should start at roughly equal widths (50/50 split)
    expect(Math.abs(box1Before!.width - box2Before!.width)).toBeLessThan(20)

    // Find the resize handle between the two tiles by position.
    // There are also sidebar resize handles on the page, so we need to
    // find the one that sits between tile 0 and tile 1.
    const handles = page.locator('[data-testid="tile-resize-handle"]')
    const handleCount = await handles.count()
    let handleBox: { x: number, y: number, width: number, height: number } | null = null
    for (let i = 0; i < handleCount; i++) {
      const hBox = await handles.nth(i).boundingBox()
      if (
        hBox
        && hBox.x >= box1Before!.x + box1Before!.width - 10
        && hBox.x <= box2Before!.x + 10
      ) {
        handleBox = hBox
        break
      }
    }
    expect(handleBox).toBeTruthy()

    // Drag the handle 80px to the right
    const hx = handleBox!.x + handleBox!.width / 2
    const hy = handleBox!.y + handleBox!.height / 2
    await page.mouse.move(hx, hy)
    await page.mouse.down()
    await page.mouse.move(hx + 80, hy, { steps: 5 })
    await page.mouse.up()

    await page.waitForTimeout(300)

    // Get new bounding boxes
    const box1After = await tiles.nth(0).boundingBox()
    const box2After = await tiles.nth(1).boundingBox()
    expect(box1After).toBeTruthy()
    expect(box2After).toBeTruthy()

    // First tile should have grown significantly
    expect(box1After!.width).toBeGreaterThan(box1Before!.width + 40)

    // Second tile should have shrunk significantly
    expect(box2After!.width).toBeLessThan(box2Before!.width - 40)

    // Both tiles should still be visible (positive width)
    expect(box1After!.width).toBeGreaterThan(50)
    expect(box2After!.width).toBeGreaterThan(50)
  })
})
