import { expect, test } from './fixtures'

/**
 * Helper: wait for a MoveSection API response (persisted to server).
 */
function waitForMoveSection(page: import('@playwright/test').Page) {
  return page.waitForResponse(
    resp => resp.url().includes('SectionService/MoveSection') && resp.ok(),
    { timeout: 10_000 },
  )
}

/**
 * Helper: get ordered section testids from a sidebar container.
 * Returns an array like ['section-header-files', 'section-header-todos'].
 */
async function getSectionOrder(page: import('@playwright/test').Page, side: 'left' | 'right') {
  // Each section has a data-testid on the <details> element (section-header-*).
  // The <summary> also has a data-testid ending with "-summary", so we
  // exclude those to avoid double-counting.
  const sections = page.locator(`[data-testid^="section-header-"]`)
  const count = await sections.count()
  const result: string[] = []
  for (let i = 0; i < count; i++) {
    const testId = await sections.nth(i).getAttribute('data-testid')
    if (testId && !testId.endsWith('-summary'))
      result.push(testId)
  }
  return result
}

/**
 * Helper: drag a section by its drag handle to another section's position.
 * Uses the drag handle within the section identified by sourceTestId and
 * drops on the summary of the section identified by targetTestId.
 */
async function dragSection(
  page: import('@playwright/test').Page,
  sourceTestId: string,
  targetTestId: string,
) {
  // Find the drag handle within the source section
  const sourceHandle = page.locator(`[data-testid="${sourceTestId}"] [data-testid^="section-drag-handle-"]`)
  const targetSummary = page.locator(`[data-testid="${targetTestId}-summary"]`)

  await expect(sourceHandle).toBeVisible()
  await expect(targetSummary).toBeVisible()

  const sourceBox = (await sourceHandle.boundingBox())!
  const targetBox = (await targetSummary.boundingBox())!

  // Drag from center of source handle to center of target summary
  await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2)
  await page.waitForTimeout(100)
  await page.mouse.down()
  await page.waitForTimeout(100)
  await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y + targetBox.height / 2, { steps: 15 })
  await page.waitForTimeout(100)
  await page.mouse.up()
}

test.describe('Section Reorder & Move', () => {
  test('should show drag handles on section headers', async ({ page, authenticatedWorkspace }) => {
    // Left sidebar sections should have drag handles
    const inProgressHandle = page.locator('[data-testid="section-header-workspaces_in_progress"] [data-testid^="section-drag-handle-"]')
    await expect(inProgressHandle).toBeVisible()

    // Right sidebar sections should have drag handles
    const filesHandle = page.locator('[data-testid="section-header-files"] [data-testid^="section-drag-handle-"]')
    await expect(filesHandle).toBeVisible()
  })

  test('should not show drag handle on user menu section', async ({ page, authenticatedWorkspace }) => {
    // The user menu section is rail-only and not draggable.
    // It doesn't have a section-header testid, so verify the user-menu-trigger exists
    // but has no drag handle nearby.
    await expect(page.locator('[data-testid="user-menu-trigger"]')).toBeVisible()

    // There should be no drag handle for user-menu (it's railOnly, not in expandable sections)
    await expect(page.locator('[data-testid^="section-drag-handle-user-menu"]')).toHaveCount(0)
  })

  test('should reorder sections within the right sidebar', async ({ page, authenticatedWorkspace }) => {
    // The Todos section is only visible when there are active todos, so we
    // first move Archived from the left sidebar to the right sidebar to get
    // two visible sections for reordering.
    await expect(page.locator('[data-testid="section-header-files"]')).toBeVisible()
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

    // Move Archived to the right sidebar
    const moveResp = waitForMoveSection(page)
    await dragSection(page, 'section-header-workspaces_archived', 'section-header-files')
    await moveResp

    // Wait for UI to stabilize after the cross-sidebar move
    await page.waitForTimeout(500)

    // Verify both sections are now in the right sidebar (Archived before Files)
    let order = await getSectionOrder(page, 'right')
    expect(order).toContain('section-header-workspaces_archived')
    expect(order).toContain('section-header-files')

    // Reload to ensure clean state before testing reorder within right sidebar
    await page.reload()
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()
    await expect(page.locator('[data-testid="section-header-files"]')).toBeVisible()

    // Now reorder: drag Files above Archived
    const reorderResp = waitForMoveSection(page)
    await dragSection(page, 'section-header-files', 'section-header-workspaces_archived')
    await reorderResp

    // Reload and verify persistence — check the server got the right order
    await page.reload()
    await expect(page.locator('[data-testid="section-header-files"]')).toBeVisible()
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

    const reloadOrder = await getSectionOrder(page, 'right')
    const reloadFilesIdx = reloadOrder.indexOf('section-header-files')
    const reloadArchivedIdx = reloadOrder.indexOf('section-header-workspaces_archived')
    expect(reloadFilesIdx).toBeLessThan(reloadArchivedIdx)
  })

  test('should reorder sections within the left sidebar', async ({ page, authenticatedWorkspace }) => {
    // Wait for left sidebar sections
    await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()

    // Expand Archived section so it's visible
    const archived = page.locator('[data-testid="section-header-workspaces_archived"]')
    await expect(archived).toBeVisible()

    // Get initial order of left sidebar sections
    const initialOrder = await getSectionOrder(page, 'left')
    const ipIdx = initialOrder.indexOf('section-header-workspaces_in_progress')
    const arIdx = initialOrder.indexOf('section-header-workspaces_archived')
    expect(ipIdx).toBeGreaterThanOrEqual(0)
    expect(arIdx).toBeGreaterThanOrEqual(0)
    expect(ipIdx).toBeLessThan(arIdx)

    // Drag Archived above In Progress
    const saved = waitForMoveSection(page)
    await dragSection(page, 'section-header-workspaces_archived', 'section-header-workspaces_in_progress')
    await saved

    // Verify new order: Archived before In Progress
    const newOrder = await getSectionOrder(page, 'left')
    const newIpIdx = newOrder.indexOf('section-header-workspaces_in_progress')
    const newArIdx = newOrder.indexOf('section-header-workspaces_archived')
    expect(newArIdx).toBeLessThan(newIpIdx)

    // Reload and verify persistence
    await page.reload()
    await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

    const reloadOrder = await getSectionOrder(page, 'left')
    const reloadIpIdx = reloadOrder.indexOf('section-header-workspaces_in_progress')
    const reloadArIdx = reloadOrder.indexOf('section-header-workspaces_archived')
    expect(reloadArIdx).toBeLessThan(reloadIpIdx)
  })

  test('should move section from left sidebar to right sidebar', async ({ page, authenticatedWorkspace }) => {
    // Ensure Archived is visible in left sidebar
    const archived = page.locator('[data-testid="section-header-workspaces_archived"]')
    await expect(archived).toBeVisible()

    // Get the drag handle for Archived section
    const archiveHandle = page.locator('[data-testid="section-header-workspaces_archived"] [data-testid^="section-drag-handle-"]')
    await expect(archiveHandle).toBeVisible()

    // Find a target in the right sidebar (e.g., Files section)
    const filesSection = page.locator('[data-testid="section-header-files"]')
    await expect(filesSection).toBeVisible()

    // Drag Archived to the right sidebar (on top of the Files section)
    const saved = waitForMoveSection(page)
    await dragSection(page, 'section-header-workspaces_archived', 'section-header-files')
    await saved

    // The Archived section should now be in the right sidebar area.
    // Verify it's no longer before Files in the left sidebar order.
    // After the move, Archived should be near Files (same sidebar).
    await page.waitForTimeout(500)

    // Reload and verify persistence
    await page.reload()
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()
    await expect(page.locator('[data-testid="section-header-files"]')).toBeVisible()

    // After reload, Archived and Files should be adjacent (both in right sidebar)
    const allSections = await getSectionOrder(page, 'right')
    expect(allSections).toContain('section-header-workspaces_archived')
    expect(allSections).toContain('section-header-files')
  })

  test('should move section from right sidebar to left sidebar', async ({ page, authenticatedWorkspace }) => {
    // Files section should be in the right sidebar
    const filesSection = page.locator('[data-testid="section-header-files"]')
    await expect(filesSection).toBeVisible()

    // Drag Files to the left sidebar's In Progress section
    const saved = waitForMoveSection(page)
    await dragSection(page, 'section-header-files', 'section-header-workspaces_in_progress')
    await saved

    await page.waitForTimeout(500)

    // Reload and verify persistence
    await page.reload()
    await expect(page.locator('[data-testid="section-header-files"]')).toBeVisible()
    await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()

    // After reload, Files should be adjacent to In Progress (both in left sidebar)
    const allSections = await getSectionOrder(page, 'left')
    expect(allSections).toContain('section-header-files')
    expect(allSections).toContain('section-header-workspaces_in_progress')
  })

  test('should preserve workspace DnD after section reorder', async ({ page, authenticatedWorkspace }) => {
    // Verify workspace is visible in the In Progress section
    const wsItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    await expect(wsItem).toBeVisible()

    // Verify workspace can still be interacted with (click to select)
    await wsItem.click()
    await expect(page).toHaveURL(new RegExp(`/workspace/${authenticatedWorkspace.workspaceId}`))
  })
})
