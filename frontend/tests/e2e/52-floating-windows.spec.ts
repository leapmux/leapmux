import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { openAgentViaUI, waitForLayoutSave } from './helpers/ui'

// ──────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────

const floatingWindows = (page: Page) => page.locator('[data-testid="floating-window"]')
const popOutButton = (page: Page) => page.locator('[data-testid="pop-out-button"]')
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

/**
 * Drag a tab element to a target element's tab bar using mouse events.
 * Uses pointer-based drag to match solid-dnd's drag sensors.
 */
async function dragTabTo(page: Page, sourceTab: ReturnType<typeof page.locator>, targetTabBar: ReturnType<typeof page.locator>) {
  const sourceBox = await sourceTab.boundingBox()
  const targetBox = await targetTabBar.boundingBox()
  expect(sourceBox).toBeTruthy()
  expect(targetBox).toBeTruthy()

  const startX = sourceBox!.x + sourceBox!.width / 2
  const startY = sourceBox!.y + sourceBox!.height / 2
  const endX = targetBox!.x + targetBox!.width / 2
  const endY = targetBox!.y + targetBox!.height / 2

  await page.mouse.move(startX, startY)
  await page.mouse.down()
  // Move in small steps to trigger DnD sensors
  await page.mouse.move(startX + 5, startY + 5, { steps: 2 })
  await page.mouse.move(endX, endY, { steps: 10 })
  await page.mouse.up()
}

test.describe('Floating Windows', () => {
  // ──────────────────────────────────────────────
  // Basic pop-out
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

  // ──────────────────────────────────────────────
  // Button visibility rules
  // ──────────────────────────────────────────────

  test('pop-out button is hidden inside floating windows', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)

    // Inside the floating window: pop-out should NOT be present
    const fw = floatingWindows(page).first()
    await expect(fw.locator('[data-testid="pop-out-button"]')).toHaveCount(0)
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

  test('closing last tab in floating window removes it', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    // Close the tab via the tab close button (not the floating window X)
    const fwTabClose = floatingWindows(page).first().locator('[data-testid="tab-close"]').first()
    await fwTabClose.click()
    await expect(floatingWindows(page)).toHaveCount(0)
  })

  test('closing floating window tab restores editor panel for main area agent', async ({ page, authenticatedWorkspace }) => {
    // Start with two agent tabs in the main area
    await openAgentViaUI(page)
    await expect(tabs(page)).toHaveCount(2)
    await expect(page.locator('[data-testid="agent-editor-panel"]')).toBeVisible()

    // Pop out the active tab into a floating window
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    // Click on the floating window tile to focus it (simulates real user interaction)
    const fwTile = floatingWindows(page).first().locator('[data-testid="tile"]')
    await fwTile.click()

    // Close the floating window tab via tab close button
    const fwTabClose = floatingWindows(page).first().locator('[data-testid="tab-close"]').first()
    await fwTabClose.click()
    await expect(floatingWindows(page)).toHaveCount(0)

    // The editor panel should be visible for the remaining main area agent tab
    await expect(page.locator('[data-testid="agent-editor-panel"]')).toBeVisible()
  })

  // ──────────────────────────────────────────────
  // Multiple floating windows
  // ──────────────────────────────────────────────

  test('can create multiple floating windows', async ({ page, authenticatedWorkspace }) => {
    // Fixture already has 1 agent; create one more so we have 2 to pop out
    await openAgentViaUI(page)
    await expect(tabs(page)).toHaveCount(2)

    // Pop out the active tab
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    // The other agent tab should still be in the main area; pop it out too
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

  test('dragging floating window snaps to left edge', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)

    const fw = floatingWindows(page).first()

    // Drag the title bar far to the left so the window's left edge nears x=0
    const titleBar = fw.locator('div').first()
    const titleBox = await titleBar.boundingBox()
    const fwBox = await fw.boundingBox()
    expect(titleBox).toBeTruthy()
    expect(fwBox).toBeTruthy()

    const startX = titleBox!.x + titleBox!.width / 2
    const startY = titleBox!.y + titleBox!.height / 2

    // Move left by the window's current x offset (plus a little extra but within snap threshold)
    await page.mouse.move(startX, startY)
    await page.mouse.down()
    await page.mouse.move(startX - fwBox!.x + 5, startY, { steps: 10 })
    await page.mouse.up()

    const geoAfter = await getFloatingWindowGeometry(page)
    // Should snap to left edge (0%)
    expect(geoAfter.left).toBe('0%')
  })

  test('dragging floating window snaps to top edge', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)

    const fw = floatingWindows(page).first()

    // Drag the title bar far upward so the window's top edge nears y=0
    const titleBar = fw.locator('div').first()
    const titleBox = await titleBar.boundingBox()
    const fwBox = await fw.boundingBox()
    expect(titleBox).toBeTruthy()
    expect(fwBox).toBeTruthy()

    const startX = titleBox!.x + titleBox!.width / 2
    const startY = titleBox!.y + titleBox!.height / 2

    // The floating window layer starts at the parent's top edge
    // Move up by the window's current y offset within the parent (plus a bit within snap threshold)
    const parentBox = await fw.evaluate((el: HTMLElement) => {
      const parent = el.parentElement!
      const rect = parent.getBoundingClientRect()
      return { x: rect.x, y: rect.y }
    })
    await page.mouse.move(startX, startY)
    await page.mouse.down()
    await page.mouse.move(startX, startY - (fwBox!.y - parentBox.y) + 5, { steps: 10 })
    await page.mouse.up()

    const geoAfter = await getFloatingWindowGeometry(page)
    // Should snap to top edge (0%)
    expect(geoAfter.top).toBe('0%')
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

  test('resizing via W handle past minimum clamps instead of snapping back', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)

    const fw = floatingWindows(page).first()
    const geoBefore = await getFloatingWindowGeometry(page)
    const fwBox = await fw.boundingBox()
    expect(fwBox).toBeTruthy()

    // Drag the W (left) edge far to the right — past the minimum width threshold
    const wX = fwBox!.x + 2
    const wY = fwBox!.y + fwBox!.height / 2
    await page.mouse.move(wX, wY)
    await page.mouse.down()
    // Move 400px to the right — more than enough to exceed minimum
    await page.mouse.move(wX + 400, wY, { steps: 10 })
    await page.mouse.up()

    const geoAfter = await getFloatingWindowGeometry(page)
    // Width should be smaller than before (clamped at minimum), not snapped back to original
    expect(Number.parseFloat(geoAfter.width)).toBeLessThan(Number.parseFloat(geoBefore.width))
  })

  test('resizing via N handle past minimum clamps instead of snapping back', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)
    await popOutActiveTab(page)

    const fw = floatingWindows(page).first()
    const geoBefore = await getFloatingWindowGeometry(page)
    const fwBox = await fw.boundingBox()
    expect(fwBox).toBeTruthy()

    // Drag the N (top) edge far downward — past the minimum height threshold
    const nX = fwBox!.x + fwBox!.width / 2
    const nY = fwBox!.y + 2
    await page.mouse.move(nX, nY)
    await page.mouse.down()
    // Move 400px down — more than enough to exceed minimum
    await page.mouse.move(nX, nY + 400, { steps: 10 })
    await page.mouse.up()

    const geoAfter = await getFloatingWindowGeometry(page)
    // Height should be smaller than before (clamped at minimum), not snapped back to original
    expect(Number.parseFloat(geoAfter.height)).toBeLessThan(Number.parseFloat(geoBefore.height))
  })

  // ──────────────────────────────────────────────
  // Cross-scope drag-and-drop
  // ──────────────────────────────────────────────

  test('drag tab from floating window to main area tab bar', async ({ page, authenticatedWorkspace }) => {
    // Fixture already has 1 agent; create one more so main area keeps one after pop-out
    await openAgentViaUI(page)
    await expect(tabs(page)).toHaveCount(2)

    // Pop out the active tab
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    const fwTab = floatingWindows(page).first().locator('[data-testid="tab"]').first()
    const mainTabBar = page.locator('[data-testid="tile"]').first().locator('[data-testid="tab-list"]')

    // Drag from floating window to main area
    await dragTabTo(page, fwTab, mainTabBar)

    // Floating window should auto-remove (now empty)
    await expect(floatingWindows(page)).toHaveCount(0)
    // All tabs should be in the main area
    await expect(tabs(page)).toHaveCount(2)
  })

  test('drag tab from main area to floating window tab bar', async ({ page, authenticatedWorkspace }) => {
    // Fixture already has 1 agent; create one more, pop one out
    await openAgentViaUI(page)
    await expect(tabs(page)).toHaveCount(2)

    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    // The remaining main tab
    const mainTab = page.locator('[data-testid="tile"]').first().locator('[data-testid="tab"]').first()
    const fwTabBar = floatingWindows(page).first().locator('[data-testid="tab-list"]')

    // Drag from main area to floating window
    await dragTabTo(page, mainTab, fwTabBar)

    // Floating window should now have 2 tabs
    const fwTabs = floatingWindows(page).first().locator('[data-testid="tab"]')
    await expect(fwTabs).toHaveCount(2)
  })

  test('empty floating window auto-removes when last tab dragged out', async ({ page, authenticatedWorkspace }) => {
    // Fixture already has 1 agent; create one more so main keeps a tab after pop-out
    await openAgentViaUI(page)
    await expect(tabs(page)).toHaveCount(2)

    // Pop out the active tab
    await popOutActiveTab(page)
    await expect(floatingWindows(page)).toHaveCount(1)

    const fwTab = floatingWindows(page).first().locator('[data-testid="tab"]').first()
    const mainTabBar = page.locator('[data-testid="tile"]').first().locator('[data-testid="tab-list"]')

    await dragTabTo(page, fwTab, mainTabBar)

    // Floating window should auto-remove
    await expect(floatingWindows(page)).toHaveCount(0)
  })
})
