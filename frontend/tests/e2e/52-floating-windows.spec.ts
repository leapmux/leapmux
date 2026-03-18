import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { openAgentViaUI, waitForLayoutSave } from './helpers/ui'

// ──────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────

const floatingWindows = (page: Page) => page.locator('[data-testid="floating-window"]')
const popOutButton = (page: Page) => page.locator('[data-testid="pop-out-button"]')
const popInButton = (page: Page) => page.locator('[data-testid="pop-in-button"]')
const tiles = (page: Page) => page.locator('[data-testid="tile"]')
const tabs = (page: Page) => page.locator('[data-testid="tab"]')

/** Pop the active tab out of the first main-area tile into a floating window. */
async function popOutActiveTab(page: Page) {
  const before = await floatingWindows(page).count()
  await popOutButton(page).first().click()
  await expect(floatingWindows(page)).toHaveCount(before + 1)
}

/** Get computed style values from a floating window element. */
async function getFloatingWindowGeometry(page: Page, index = 0) {
  const fw = floatingWindows(page).nth(index)
  return fw.evaluate((el: HTMLElement) => {
    const style = el.style
    return {
      left: style.left,
      top: style.top,
      width: style.width,
      height: style.height,
    }
  })
}

test.describe('Floating Windows', () => {
  // ──────────────────────────────────────────────
  // Basic pop-out / pop-in
  // ──────────────────────────────────────────────

  test('pop-out button creates a floating window', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Pop-out button should be visible in the main area
    await expect(popOutButton(page).first()).toBeVisible()
    // No floating windows yet
    await expect(floatingWindows(page)).toHaveCount(0)

    await popOutActiveTab(page)

    // Floating window layer should contain exactly one window
    await expect(floatingWindows(page)).toHaveCount(1)
    // The floating window should contain a tile with a tab
    const fwTiles = floatingWindows(page).first().locator('[data-testid="tile"]')
    await expect(fwTiles).toHaveCount(1)
    const fwTabs = floatingWindows(page).first().locator('[data-testid="tab"]')
    await expect(fwTabs).toHaveCount(1)
  })

  test('pop-in button moves tab back to main area', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    const initialTabCount = await tabs(page).count()

    // Pop out
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    // Pop-in button should be visible inside the floating window
    const fwPopIn = floatingWindows(page).first().locator('[data-testid="pop-in-button"]')
    await expect(fwPopIn).toBeVisible()

    // Pop in
    await fwPopIn.click()

    // Floating window should be removed (empty after tab moved out)
    await expect(floatingWindows(page)).toHaveCount(0)
    // Tab should be back in the main area
    await expect(tabs(page)).toHaveCount(initialTabCount)
  })

  test('pop-out then pop-in round-trip preserves the tab', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Remember the tab title
    const tabTitle = await tabs(page).first().textContent()

    // Pop out and back in
    await popOutActiveTab(page)
    await floatingWindows(page).first().locator('[data-testid="pop-in-button"]').click()
    await expect(floatingWindows(page)).toHaveCount(0)

    // The tab should still be present in the main area with the same title
    await expect(tabs(page).first()).toContainText(tabTitle)
  })

  // ──────────────────────────────────────────────
  // Button visibility rules
  // ──────────────────────────────────────────────

  test('pop-out button is hidden inside floating windows', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)

    // Inside the floating window: pop-out should NOT be present, pop-in should
    const fw = floatingWindows(page).first()
    await expect(fw.locator('[data-testid="pop-out-button"]')).toHaveCount(0)
    await expect(fw.locator('[data-testid="pop-in-button"]')).toBeVisible()
  })

  test('pop-in button is hidden in main area tiles', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Main area: pop-in should NOT be present, pop-out should
    await expect(popInButton(page)).toHaveCount(0)
    await expect(popOutButton(page).first()).toBeVisible()
  })

  test('pop-out button hidden when tile has no active tab', async ({ page, authenticatedWorkspace }) => {
    // Default workspace has one agent tab in one tile.
    // Split to create an empty tile, then check the empty tile has no pop-out.
    await openAgentViaUI(page)
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(tiles(page)).toHaveCount(2)

    // The second tile is empty (no tabs); its pop-out button should be absent
    const secondTile = tiles(page).nth(1)
    await expect(secondTile.locator('[data-testid="pop-out-button"]')).toHaveCount(0)
  })

  // ──────────────────────────────────────────────
  // Close floating window
  // ──────────────────────────────────────────────

  test('closing a floating window via title bar X removes it', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    await floatingWindows(page).first().locator('[data-testid="floating-window-close"]').click()
    await expect(floatingWindows(page)).toHaveCount(0)
  })

  // ──────────────────────────────────────────────
  // Multiple floating windows
  // ──────────────────────────────────────────────

  test('can create multiple floating windows', async ({ page, authenticatedWorkspace }) => {
    // Create two agents, pop each out
    await openAgentViaUI(page)
    await openAgentViaUI(page)
    await expect(tabs(page)).toHaveCount(2)

    // Pop out the active tab (second agent)
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    // The first agent tab should still be in the main area; pop it out too
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(2)
  })

  // ──────────────────────────────────────────────
  // Drag to move
  // ──────────────────────────────────────────────

  test('dragging title bar moves the floating window', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)

    const fw = floatingWindows(page).first()
    const geoBefore = await getFloatingWindowGeometry(page)

    // Drag the title bar 100px right and 50px down
    const titleBar = fw.locator('div').first() // title bar is first child div
    const titleBox = await titleBar.boundingBox()
    expect(titleBox).toBeTruthy()

    const startX = titleBox!.x + titleBox!.width / 2
    const startY = titleBox!.y + titleBox!.height / 2

    await page.mouse.move(startX, startY)
    await page.mouse.down()
    await page.mouse.move(startX + 100, startY + 50, { steps: 5 })
    await page.mouse.up()

    const geoAfter = await getFloatingWindowGeometry(page)
    // Position should have changed
    expect(geoAfter.left).not.toBe(geoBefore.left)
    expect(geoAfter.top).not.toBe(geoBefore.top)
    // Size should not change from dragging
    expect(geoAfter.width).toBe(geoBefore.width)
    expect(geoAfter.height).toBe(geoBefore.height)
  })

  // ──────────────────────────────────────────────
  // Resize
  // ──────────────────────────────────────────────

  test('resizing via SE handle changes width and height', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)

    const fw = floatingWindows(page).first()
    const geoBefore = await getFloatingWindowGeometry(page)

    // Find the SE resize handle — it's the last resize handle div
    const fwBox = await fw.boundingBox()
    expect(fwBox).toBeTruthy()

    // The SE resize handle is at the bottom-right corner
    const seX = fwBox!.x + fwBox!.width - 2
    const seY = fwBox!.y + fwBox!.height - 2

    await page.mouse.move(seX, seY)
    await page.mouse.down()
    await page.mouse.move(seX + 80, seY + 60, { steps: 5 })
    await page.mouse.up()

    const geoAfter = await getFloatingWindowGeometry(page)
    // Size should have changed
    expect(geoAfter.width).not.toBe(geoBefore.width)
    expect(geoAfter.height).not.toBe(geoBefore.height)
    // Position (top-left) should stay the same for SE resize
    expect(geoAfter.left).toBe(geoBefore.left)
    expect(geoAfter.top).toBe(geoBefore.top)
  })

  // ──────────────────────────────────────────────
  // Persistence
  // ──────────────────────────────────────────────

  test('floating window persists across page reload', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    await waitForLayoutSave(page)
    await page.reload()
    await page.locator('[data-testid="tile"]').first().waitFor({ timeout: 10_000 })

    // Floating window should be restored
    await expect(floatingWindows(page)).toHaveCount(1)
    // It should contain a tile with a tab
    const fwTabs = floatingWindows(page).first().locator('[data-testid="tab"]')
    await expect(fwTabs).toHaveCount(1)
  })

  test('floating window position persists after drag and reload', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)

    // Drag the window
    const fw = floatingWindows(page).first()
    const titleBar = fw.locator('div').first()
    const titleBox = await titleBar.boundingBox()
    expect(titleBox).toBeTruthy()

    const startX = titleBox!.x + titleBox!.width / 2
    const startY = titleBox!.y + titleBox!.height / 2
    await page.mouse.move(startX, startY)
    await page.mouse.down()
    await page.mouse.move(startX + 120, startY + 80, { steps: 5 })
    await page.mouse.up()

    const geoAfterDrag = await getFloatingWindowGeometry(page)

    await waitForLayoutSave(page)
    await page.reload()
    await page.locator('[data-testid="tile"]').first().waitFor({ timeout: 10_000 })
    await expect(floatingWindows(page)).toHaveCount(1)

    const geoAfterReload = await getFloatingWindowGeometry(page)
    // Position should be preserved (comparing percentage strings)
    expect(geoAfterReload.left).toBe(geoAfterDrag.left)
    expect(geoAfterReload.top).toBe(geoAfterDrag.top)
  })

  test('floating window size persists after resize and reload', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)

    // Resize via SE corner
    const fw = floatingWindows(page).first()
    const fwBox = await fw.boundingBox()
    expect(fwBox).toBeTruthy()

    const seX = fwBox!.x + fwBox!.width - 2
    const seY = fwBox!.y + fwBox!.height - 2
    await page.mouse.move(seX, seY)
    await page.mouse.down()
    await page.mouse.move(seX + 100, seY + 80, { steps: 5 })
    await page.mouse.up()

    const geoAfterResize = await getFloatingWindowGeometry(page)

    await waitForLayoutSave(page)
    await page.reload()
    await page.locator('[data-testid="tile"]').first().waitFor({ timeout: 10_000 })
    await expect(floatingWindows(page)).toHaveCount(1)

    const geoAfterReload = await getFloatingWindowGeometry(page)
    expect(geoAfterReload.width).toBe(geoAfterResize.width)
    expect(geoAfterReload.height).toBe(geoAfterResize.height)
  })

  // ──────────────────────────────────────────────
  // Context menu: Move to Main Area / Move to Window
  // ──────────────────────────────────────────────

  test('right-click context menu shows "Move to Main Area" inside floating window', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)

    // Right-click the tab inside the floating window
    const fwTab = floatingWindows(page).first().locator('[data-testid="tab"]').first()
    await fwTab.click({ button: 'right' })

    // Context menu should have "Move to Main Area"
    await expect(page.getByRole('menuitem', { name: 'Move to Main Area' })).toBeVisible()
  })

  test('right-click "Move to Main Area" works like pop-in', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    // Right-click tab and choose "Move to Main Area"
    const fwTab = floatingWindows(page).first().locator('[data-testid="tab"]').first()
    await fwTab.click({ button: 'right' })
    await page.getByRole('menuitem', { name: 'Move to Main Area' }).click()

    // Floating window should be gone
    await expect(floatingWindows(page)).toHaveCount(0)
  })

  test('right-click context menu shows "Move to New Window" in main area', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    // Right-click the tab in the main area
    const mainTab = tabs(page).first()
    await mainTab.click({ button: 'right' })

    // Context menu should have "Move to New Window"
    await expect(page.getByRole('menuitem', { name: 'Move to New Window' })).toBeVisible()
  })

  // ──────────────────────────────────────────────
  // Edge case: pop-in targets main layout tile
  // ──────────────────────────────────────────────

  test('pop-in moves tab to main layout even after clicking inside floating window', async ({ page, authenticatedWorkspace }) => {
    // This tests the fix where clicking a floating window tile used to set
    // layoutStore.focusedTileId to the floating window's tile, causing pop-in
    // to move the tab back to the same floating window tile (no-op).
    await openAgentViaUI(page)
    await openAgentViaUI(page)
    const tabCount = await tabs(page).count()

    // Pop out one tab
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    // Click inside the floating window to focus it (simulating user interaction)
    const fwTile = floatingWindows(page).first().locator('[data-testid="tile"]').first()
    await fwTile.click()

    // Now pop in
    await floatingWindows(page).first().locator('[data-testid="pop-in-button"]').click()
    await expect(floatingWindows(page)).toHaveCount(0)

    // All tabs should be in the main area
    await expect(tabs(page)).toHaveCount(tabCount)
  })
})
