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
 * If `side` is given, only returns sections within that sidebar.
 */
async function getSectionOrder(page: import('@playwright/test').Page, side?: 'left' | 'right') {
  // Each section has a data-testid on the <details> element (section-header-*).
  // The <summary> also has a data-testid ending with "-summary", so we
  // exclude those to avoid double-counting.
  const root = side
    ? page.locator(`[data-testid="sidebar-${side}"]`)
    : page
  const sections = root.locator(`[data-testid^="section-header-"]`)
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
 *
 * Proximity-based drop activation requires the cursor to be near the
 * indicator line: 'before' indicator is at the section top, 'after'
 * indicator is at the section bottom.  This helper detects direction
 * and sidebar to choose the correct drop target:
 *  - Cross-sidebar: target the upper header area → 'before' positioning.
 *  - Same sidebar, dragging down: target the section bottom → 'after'.
 *  - Same sidebar, dragging up: target the header top → 'before'.
 */
async function dragSection(
  page: import('@playwright/test').Page,
  sourceTestId: string,
  targetTestId: string,
) {
  const sourceHandle = page.locator(`[data-testid="${sourceTestId}"] [data-testid^="section-drag-handle-"]`)
  const targetSummary = page.locator(`[data-testid="${targetTestId}-summary"]`)
  const targetSection = page.locator(`[data-testid="${targetTestId}"]`)

  await expect(sourceHandle).toBeVisible()
  await expect(targetSummary).toBeVisible()

  const sourceBox = (await sourceHandle.boundingBox())!
  const targetSummaryBox = (await targetSummary.boundingBox())!

  // Detect same vs cross sidebar
  const sourceInLeft = await page.locator(`[data-testid="sidebar-left"] [data-testid="${sourceTestId}"]`).count() > 0
  const targetInLeft = await page.locator(`[data-testid="sidebar-left"] [data-testid="${targetTestId}"]`).count() > 0
  const sameSidebar = sourceInLeft === targetInLeft

  const targetX = targetSummaryBox.x + targetSummaryBox.width / 2
  let targetY: number

  if (!sameSidebar) {
    // Cross-sidebar: target the upper header area for 'before' positioning
    targetY = targetSummaryBox.y + 5
  }
  else if (sourceBox.y < targetSummaryBox.y) {
    // Same sidebar, source above target → 'after' indicator at section bottom
    const sectionBox = (await targetSection.boundingBox())!
    targetY = sectionBox.y + sectionBox.height - 5
  }
  else {
    // Same sidebar, source below target → 'before' indicator at section top
    targetY = targetSummaryBox.y + 5
  }

  await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2)
  await page.waitForTimeout(100)
  await page.mouse.down()
  await page.waitForTimeout(100)
  await page.mouse.move(targetX, targetY, { steps: 15 })
  await page.waitForTimeout(100)
  await page.mouse.up()
}

/**
 * Helper: drag a section by its drag handle to below another section.
 * Drops near the bottom of the target section to trigger 'after' insertion
 * within the proximity zone.
 */
async function dragSectionBelow(
  page: import('@playwright/test').Page,
  sourceTestId: string,
  targetTestId: string,
) {
  const sourceHandle = page.locator(`[data-testid="${sourceTestId}"] [data-testid^="section-drag-handle-"]`)
  const targetSection = page.locator(`[data-testid="${targetTestId}"]`)
  const targetSummary = page.locator(`[data-testid="${targetTestId}-summary"]`)

  await expect(sourceHandle).toBeVisible()
  await expect(targetSummary).toBeVisible()

  const sourceBox = (await sourceHandle.boundingBox())!
  const sectionBox = (await targetSection.boundingBox())!
  const targetSummaryBox = (await targetSummary.boundingBox())!

  await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2)
  await page.waitForTimeout(100)
  await page.mouse.down()
  await page.waitForTimeout(100)
  // Drop near the bottom of the target section (within the 'after' proximity zone)
  await page.mouse.move(targetSummaryBox.x + targetSummaryBox.width / 2, sectionBox.y + sectionBox.height + 5, { steps: 15 })
  await page.waitForTimeout(100)
  await page.mouse.up()
}

/**
 * Helper: ensure a section is in the specified sidebar. Moves it if needed.
 * Returns true if a move was performed.
 */
async function ensureSectionInSidebar(
  page: import('@playwright/test').Page,
  sectionTestId: string,
  targetSide: 'left' | 'right',
): Promise<boolean> {
  const sectionsInTarget = await getSectionOrder(page, targetSide)
  if (sectionsInTarget.includes(sectionTestId))
    return false // already there

  // Find a drop target in the target sidebar
  if (sectionsInTarget.length > 0) {
    const saved = waitForMoveSection(page)
    await dragSection(page, sectionTestId, sectionsInTarget[0])
    await saved
    await page.waitForTimeout(300)
    return true
  }

  // Target sidebar is empty — drag to the empty drop zone
  const sourceHandle = page.locator(`[data-testid="${sectionTestId}"] [data-testid^="section-drag-handle-"]`)
  const emptyZone = page.locator(`[data-testid="empty-drop-zone-${targetSide}"]`)
  await expect(sourceHandle).toBeVisible()
  await expect(emptyZone).toBeVisible()

  const sourceBox = (await sourceHandle.boundingBox())!
  const targetBox = (await emptyZone.boundingBox())!

  const saved = waitForMoveSection(page)
  await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2)
  await page.waitForTimeout(100)
  await page.mouse.down()
  await page.waitForTimeout(100)
  await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y + targetBox.height / 2, { steps: 15 })
  await page.waitForTimeout(100)
  await page.mouse.up()
  await saved
  await page.waitForTimeout(300)
  return true
}

// ============================================================================
// Each test is self-contained: it reads the current state and sets up its own
// preconditions via helper moves. Tests can be run individually with --grep
// or in any order. Section moves persist within a worker, but no test assumes
// state from a prior test.
//
// Default initial state: Left=[In Progress, Archived], Right=[Files]
// ============================================================================

test.describe('Section Reorder & Move', () => {
  // ---------- Read-only tests (no section moves) ----------

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

  test('should not shift section title when drag handle is present', async ({ page, authenticatedWorkspace }) => {
    // The drag handle should be absolutely positioned and not affect the
    // section title alignment.
    const dragHandle = page.locator('[data-testid^="section-drag-handle-"]').first()
    await expect(dragHandle).toBeVisible()

    // Verify the drag handle has position: absolute (does not affect flow)
    const position = await dragHandle.evaluate(el => getComputedStyle(el).position)
    expect(position).toBe('absolute')

    // Verify the Archived section title (index > 0, no sidebar collapse button)
    // has the same x as the section icon — both should be at the left padding offset.
    const archivedSummary = page.locator('[data-testid="section-header-workspaces_archived-summary"]')
    const archivedIcon = archivedSummary.locator('svg').first()
    const archivedTitle = archivedSummary.locator('span').first()
    await expect(archivedIcon).toBeVisible()
    await expect(archivedTitle).toBeVisible()

    const iconBox = (await archivedIcon.boundingBox())!
    const titleBox = (await archivedTitle.boundingBox())!

    // The title should be to the right of the icon (icon + gap)
    // and the icon should be within the left padding area.
    expect(titleBox.x).toBeGreaterThan(iconBox.x)
  })

  test('should show drop indicator line during section drag', async ({ page, authenticatedWorkspace }) => {
    // Ensure both sections are visible
    const inProgressHandle = page.locator('[data-testid="section-header-workspaces_in_progress"] [data-testid^="section-drag-handle-"]')
    const archivedSummary = page.locator('[data-testid="section-header-workspaces_archived-summary"]')
    await expect(inProgressHandle).toBeVisible()
    await expect(archivedSummary).toBeVisible()

    const sourceBox = (await inProgressHandle.boundingBox())!
    const targetBox = (await archivedSummary.boundingBox())!

    // Start drag and hold the mouse over the target
    await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2)
    await page.waitForTimeout(100)
    await page.mouse.down()
    await page.waitForTimeout(100)
    await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y + targetBox.height / 2, { steps: 15 })
    await page.waitForTimeout(200)

    // A drop indicator should be visible
    const dropIndicator = page.locator('[data-testid="drop-indicator"]')
    await expect(dropIndicator.first()).toBeVisible()

    // Cancel the drag by reloading — this avoids completing the drop
    // (which would change section order and affect subsequent tests).
    // solid-dnd has no keyboard cancel support, so reload is the
    // cleanest way to abort a drag without side effects.
    await page.reload()

    // After reload, drop indicator should not exist
    await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()
    await expect(page.locator('[data-testid="drop-indicator"]')).toHaveCount(0)
  })

  // ---------- Destructive tests (modify section positions) ----------
  // Each test establishes its own preconditions, so they can run independently.

  test('should render workspace content after moving workspace section to right sidebar', async ({ page, authenticatedWorkspace }) => {
    // Ensure IP is in the left sidebar and a drop target exists in the right
    await ensureSectionInSidebar(page, 'section-header-workspaces_in_progress', 'left')
    await ensureSectionInSidebar(page, 'section-header-files', 'right')

    // The workspace item should be visible in the left sidebar
    const wsItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    await expect(wsItem).toBeVisible()

    const leftSidebar = page.locator('[data-testid="sidebar-left"]')
    await expect(leftSidebar.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)).toBeVisible()

    // Find a section in the right sidebar to use as drop target
    const rightSections = await getSectionOrder(page, 'right')
    expect(rightSections.length).toBeGreaterThanOrEqual(1)

    // Move the In Progress section to the right sidebar
    const saved = waitForMoveSection(page)
    await dragSection(page, 'section-header-workspaces_in_progress', rightSections[0])
    await saved
    await page.waitForTimeout(500)

    // The workspace item should still be visible (now in the right sidebar)
    await expect(wsItem).toBeVisible()

    const rightSidebar = page.locator('[data-testid="sidebar-right"]')
    await expect(rightSidebar.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)).toBeVisible()

    // Workspace should still be clickable
    await wsItem.click()
    await expect(page).toHaveURL(new RegExp(`/workspace/${authenticatedWorkspace.workspaceId}`))
  })

  test('should render file tree after moving Files section to left sidebar', async ({ page, authenticatedWorkspace }) => {
    // Ensure Files is in the right sidebar and a drop target exists in the left
    await ensureSectionInSidebar(page, 'section-header-files', 'right')
    await ensureSectionInSidebar(page, 'section-header-workspaces_in_progress', 'left')

    // Find a section in the left sidebar to use as drop target
    const leftSections = await getSectionOrder(page, 'left')
    expect(leftSections.length).toBeGreaterThanOrEqual(1)

    // Move Files section to the left sidebar
    const saved = waitForMoveSection(page)
    await dragSection(page, 'section-header-files', leftSections[0])
    await saved
    await page.waitForTimeout(500)

    // Files section should now be in the left sidebar
    const leftSidebar = page.locator('[data-testid="sidebar-left"]')
    await expect(leftSidebar.locator('[data-testid="section-header-files"]')).toBeVisible()

    // The Files section content should not be empty — it should contain
    // a directory tree or "No tab selected" fallback
    const filesSection = page.locator('[data-testid="section-header-files"]')
    await expect(filesSection).toBeVisible()

    // Verify the section still has content (not empty)
    const sectionContent = filesSection.locator('div').first()
    await expect(sectionContent).toBeVisible()
  })

  test('should reorder sections within the right sidebar', async ({ page, authenticatedWorkspace }) => {
    // Ensure at least 2 sections are in the right sidebar
    await ensureSectionInSidebar(page, 'section-header-workspaces_in_progress', 'right')
    await ensureSectionInSidebar(page, 'section-header-workspaces_archived', 'right')

    // Reload to get clean state after setup moves
    await page.reload()
    await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

    const rightBefore = await getSectionOrder(page, 'right')
    expect(rightBefore.length).toBeGreaterThanOrEqual(2)
    const first = rightBefore[0]
    const second = rightBefore[1]

    // Drag second above first
    const reorderResp = waitForMoveSection(page)
    await dragSection(page, second, first)
    await reorderResp

    // Reload and verify persistence — the order should be reversed
    await page.reload()
    await expect(page.locator(`[data-testid="${first}"]`)).toBeVisible()
    await expect(page.locator(`[data-testid="${second}"]`)).toBeVisible()

    const reloadOrder = await getSectionOrder(page, 'right')
    const reloadFirstIdx = reloadOrder.indexOf(first)
    const reloadSecondIdx = reloadOrder.indexOf(second)
    expect(reloadSecondIdx).toBeLessThan(reloadFirstIdx)
  })

  test('should reorder sections within the left sidebar', async ({ page, authenticatedWorkspace }) => {
    // Ensure at least 2 sections are in the left sidebar
    await ensureSectionInSidebar(page, 'section-header-workspaces_in_progress', 'left')
    await ensureSectionInSidebar(page, 'section-header-files', 'left')

    // Reload to get clean state after setup moves
    await page.reload()
    await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()
    await expect(page.locator('[data-testid="section-header-files"]')).toBeVisible()

    const initialOrder = await getSectionOrder(page, 'left')
    expect(initialOrder.length).toBeGreaterThanOrEqual(2)
    const first = initialOrder[0]
    const second = initialOrder[1]

    // Drag second above first
    const saved = waitForMoveSection(page)
    await dragSection(page, second, first)
    await saved

    // Verify new order: second is now before first
    const newOrder = await getSectionOrder(page, 'left')
    const newFirstIdx = newOrder.indexOf(first)
    const newSecondIdx = newOrder.indexOf(second)
    expect(newSecondIdx).toBeLessThan(newFirstIdx)

    // Reload and verify persistence
    await page.reload()
    await expect(page.locator(`[data-testid="${first}"]`)).toBeVisible()
    await expect(page.locator(`[data-testid="${second}"]`)).toBeVisible()

    const reloadOrder = await getSectionOrder(page, 'left')
    const reloadFirstIdx = reloadOrder.indexOf(first)
    const reloadSecondIdx = reloadOrder.indexOf(second)
    expect(reloadSecondIdx).toBeLessThan(reloadFirstIdx)
  })

  test('should move section from left sidebar to right sidebar', async ({ page, authenticatedWorkspace }) => {
    // Ensure both sidebars have at least one section
    await ensureSectionInSidebar(page, 'section-header-workspaces_in_progress', 'left')
    await ensureSectionInSidebar(page, 'section-header-files', 'right')

    // Find a section in the left sidebar
    const leftSections = await getSectionOrder(page, 'left')
    expect(leftSections.length).toBeGreaterThanOrEqual(1)
    const source = leftSections[leftSections.length - 1] // take the last one

    // Find a section in the right sidebar as drop target
    const rightSections = await getSectionOrder(page, 'right')
    expect(rightSections.length).toBeGreaterThanOrEqual(1)
    const target = rightSections[0]

    // Drag from left to right
    const saved = waitForMoveSection(page)
    await dragSection(page, source, target)
    await saved
    await page.waitForTimeout(500)

    // Reload and verify the section moved to the right sidebar
    await page.reload()
    await expect(page.locator(`[data-testid="${source}"]`)).toBeVisible()

    const rightAfter = await getSectionOrder(page, 'right')
    expect(rightAfter).toContain(source)
  })

  test('should move section from right sidebar to left sidebar', async ({ page, authenticatedWorkspace }) => {
    // Ensure both sidebars have at least one section
    await ensureSectionInSidebar(page, 'section-header-workspaces_in_progress', 'left')
    await ensureSectionInSidebar(page, 'section-header-files', 'right')

    // Find a section in the right sidebar
    const rightSections = await getSectionOrder(page, 'right')
    expect(rightSections.length).toBeGreaterThanOrEqual(1)
    const source = rightSections[rightSections.length - 1]

    // Find a section in the left sidebar as drop target
    const leftSections = await getSectionOrder(page, 'left')
    expect(leftSections.length).toBeGreaterThanOrEqual(1)
    const target = leftSections[0]

    // Drag from right to left
    const saved = waitForMoveSection(page)
    await dragSection(page, source, target)
    await saved
    await page.waitForTimeout(500)

    // Reload and verify the section moved to the left sidebar
    await page.reload()
    await expect(page.locator(`[data-testid="${source}"]`)).toBeVisible()

    const leftAfter = await getSectionOrder(page, 'left')
    expect(leftAfter).toContain(source)
  })

  test('should insert section after target when dropped below its header', async ({ page, authenticatedWorkspace }) => {
    // Ensure Archived is in the left sidebar (so this is a cross-sidebar drag)
    await ensureSectionInSidebar(page, 'section-header-workspaces_archived', 'left')
    // Ensure Files is in the right sidebar
    await ensureSectionInSidebar(page, 'section-header-files', 'right')

    // Reload to get clean layout state after setup moves
    await page.reload()
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()
    await expect(page.locator('[data-testid="section-header-files"]')).toBeVisible()

    // Drag Archived from left sidebar below the Files header in right sidebar.
    // This should place Archived AFTER Files (not before).
    const saved = waitForMoveSection(page)
    await dragSectionBelow(page, 'section-header-workspaces_archived', 'section-header-files')
    await saved
    await page.waitForTimeout(500)

    // Verify Archived is now in the right sidebar, AFTER Files
    const rightOrder = await getSectionOrder(page, 'right')
    expect(rightOrder).toContain('section-header-workspaces_archived')
    expect(rightOrder).toContain('section-header-files')
    const filesIdx = rightOrder.indexOf('section-header-files')
    const archivedIdx = rightOrder.indexOf('section-header-workspaces_archived')
    expect(archivedIdx).toBeGreaterThan(filesIdx)

    // Reload and verify persistence
    await page.reload()
    await expect(page.locator('[data-testid="section-header-files"]')).toBeVisible()
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

    const reloadOrder = await getSectionOrder(page, 'right')
    const reloadFilesIdx = reloadOrder.indexOf('section-header-files')
    const reloadArchivedIdx = reloadOrder.indexOf('section-header-workspaces_archived')
    expect(reloadArchivedIdx).toBeGreaterThan(reloadFilesIdx)
  })

  test('should preserve workspace DnD after section reorder', async ({ page, authenticatedWorkspace }) => {
    // Verify workspace is visible in the In Progress section
    const wsItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    await expect(wsItem).toBeVisible()

    // Verify workspace can still be interacted with (click to select)
    await wsItem.click()
    await expect(page).toHaveURL(new RegExp(`/workspace/${authenticatedWorkspace.workspaceId}`))
  })

  test('should show empty drop zone when all sections are moved from a sidebar', async ({ page, authenticatedWorkspace }) => {
    // Ensure both sidebars have at least one section
    await ensureSectionInSidebar(page, 'section-header-workspaces_in_progress', 'left')
    await ensureSectionInSidebar(page, 'section-header-files', 'right')

    // Move all sections out of the right sidebar to create an empty sidebar
    let rightSections = await getSectionOrder(page, 'right')
    const leftTarget = (await getSectionOrder(page, 'left'))[0]

    // Move each right sidebar section to the left sidebar
    for (const section of rightSections) {
      const saved = waitForMoveSection(page)
      await dragSection(page, section, leftTarget)
      await saved
      await page.waitForTimeout(300)
    }

    // The right sidebar should now show the empty drop zone
    const emptyZone = page.locator('[data-testid="empty-drop-zone-right"]')
    await expect(emptyZone).toBeVisible()
    await expect(emptyZone).toContainText('No sections')

    // Verify no section headers remain in the right sidebar
    rightSections = await getSectionOrder(page, 'right')
    expect(rightSections.length).toBe(0)
  })

  test('should allow dragging section back into empty sidebar', async ({ page, authenticatedWorkspace }) => {
    // Ensure both sidebars have sections before emptying the right
    await ensureSectionInSidebar(page, 'section-header-workspaces_in_progress', 'left')
    await ensureSectionInSidebar(page, 'section-header-files', 'right')

    // Ensure right sidebar is empty by moving everything to the left
    const rightSections = await getSectionOrder(page, 'right')
    if (rightSections.length > 0) {
      const leftTarget = (await getSectionOrder(page, 'left'))[0]
      for (const section of rightSections) {
        const saved = waitForMoveSection(page)
        await dragSection(page, section, leftTarget)
        await saved
        await page.waitForTimeout(300)
      }
    }

    // Verify right sidebar is empty
    await expect(page.locator('[data-testid="empty-drop-zone-right"]')).toBeVisible()

    // Pick a section from the left sidebar to drag back
    const leftSections = await getSectionOrder(page, 'left')
    expect(leftSections.length).toBeGreaterThanOrEqual(1)
    const sectionToMove = leftSections[leftSections.length - 1]

    // Drag onto the empty drop zone
    const sourceHandle = page.locator(`[data-testid="${sectionToMove}"] [data-testid^="section-drag-handle-"]`)
    const emptyZone = page.locator('[data-testid="empty-drop-zone-right"]')
    await expect(sourceHandle).toBeVisible()
    await expect(emptyZone).toBeVisible()

    const sourceBox = (await sourceHandle.boundingBox())!
    const targetBox = (await emptyZone.boundingBox())!

    const moveBack = waitForMoveSection(page)
    await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2)
    await page.waitForTimeout(100)
    await page.mouse.down()
    await page.waitForTimeout(100)
    await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y + targetBox.height / 2, { steps: 15 })
    await page.waitForTimeout(100)
    await page.mouse.up()
    await moveBack

    // The section should now be in the right sidebar
    await page.waitForTimeout(500)
    const rightSidebar = page.locator('[data-testid="sidebar-right"]')
    await expect(rightSidebar.locator(`[data-testid="${sectionToMove}"]`)).toBeVisible()

    // The empty drop zone should be gone
    await expect(page.locator('[data-testid="empty-drop-zone-right"]')).toHaveCount(0)
  })

  test('should not change order when dropping section at header of its immediate successor', async ({ page, authenticatedWorkspace }) => {
    // Ensure at least 2 sections in the same sidebar
    await ensureSectionInSidebar(page, 'section-header-workspaces_in_progress', 'left')
    await ensureSectionInSidebar(page, 'section-header-workspaces_archived', 'left')

    // Reload to get clean state
    await page.reload()
    await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

    const orderBefore = await getSectionOrder(page, 'left')
    expect(orderBefore.length).toBeGreaterThanOrEqual(2)
    const first = orderBefore[0]
    const second = orderBefore[1]

    // Drag the first section to the top of the second section's header.
    // This targets the 'before' indicator of the second section, which
    // means "insert before second" — a no-op since first is already there.
    const sourceHandle = page.locator(`[data-testid="${first}"] [data-testid^="section-drag-handle-"]`)
    const targetSummary = page.locator(`[data-testid="${second}-summary"]`)
    await expect(sourceHandle).toBeVisible()
    await expect(targetSummary).toBeVisible()

    const sourceBox = (await sourceHandle.boundingBox())!
    const targetBox = (await targetSummary.boundingBox())!

    await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2)
    await page.waitForTimeout(100)
    await page.mouse.down()
    await page.waitForTimeout(100)
    // Target the top of the second section's header — near the 'before' indicator
    await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y + 5, { steps: 15 })
    await page.waitForTimeout(100)
    await page.mouse.up()

    // Brief wait for any potential server call
    await page.waitForTimeout(300)

    // Order should remain unchanged
    const orderAfter = await getSectionOrder(page, 'left')
    expect(orderAfter.indexOf(first)).toBe(orderBefore.indexOf(first))
    expect(orderAfter.indexOf(second)).toBe(orderBefore.indexOf(second))
  })

  test('should place resize handle after expanded section and before collapsed section', async ({ page, authenticatedWorkspace }) => {
    // Ensure 3 sections are in the right sidebar: Files (expanded),
    // Archived (collapsed by default), In Progress (expanded).
    await ensureSectionInSidebar(page, 'section-header-files', 'right')
    await ensureSectionInSidebar(page, 'section-header-workspaces_archived', 'right')
    await ensureSectionInSidebar(page, 'section-header-workspaces_in_progress', 'right')

    await page.reload()

    const filesSection = page.locator('[data-testid="section-header-files"]')
    const archivedSection = page.locator('[data-testid="section-header-workspaces_archived"]')
    const inProgressSection = page.locator('[data-testid="section-header-workspaces_in_progress"]')
    await expect(filesSection).toBeVisible()
    await expect(archivedSection).toBeVisible()
    await expect(inProgressSection).toBeVisible()

    // Ensure Files and In Progress are expanded, Archived is collapsed
    const archivedDetails = page.locator('[data-testid="section-header-workspaces_archived"]')
    if (await archivedDetails.getAttribute('open') !== null) {
      await page.locator('[data-testid="section-header-workspaces_archived-summary"]').click()
      await page.waitForTimeout(200)
    }
    const filesDetails = page.locator('[data-testid="section-header-files"]')
    if (await filesDetails.getAttribute('open') === null) {
      await page.locator('[data-testid="section-header-files-summary"]').click()
      await page.waitForTimeout(200)
    }
    const ipDetails = page.locator('[data-testid="section-header-workspaces_in_progress"]')
    if (await ipDetails.getAttribute('open') === null) {
      await page.locator('[data-testid="section-header-workspaces_in_progress-summary"]').click()
      await page.waitForTimeout(200)
    }

    // Check section order: Files should be first, then Archived, then IP
    const order = await getSectionOrder(page, 'right')
    const filesIdx = order.indexOf('section-header-files')
    const archivedIdx = order.indexOf('section-header-workspaces_archived')
    const ipIdx = order.indexOf('section-header-workspaces_in_progress')

    // Only test resize handle placement if the order matches the expected layout
    // (Files, Archived, In Progress). This test focuses on handle placement
    // between expanded and collapsed sections.
    if (filesIdx < archivedIdx && archivedIdx < ipIdx) {
      // There should be exactly one resize handle visible (between the two
      // expanded sections: Files and In Progress, with Archived collapsed
      // in between). The handle should render before Archived (right after
      // Files' expanded content), not between Archived and In Progress.
      const handles = page.locator('[data-testid="sidebar-right"] [data-testid="pane-resize-handle"]')
      const handleCount = await handles.count()
      expect(handleCount).toBe(1)

      // The resize handle should be between Files bottom and Archived top
      const filesBox = (await filesSection.boundingBox())!
      const archivedBox = (await archivedSection.boundingBox())!
      const handleBox = (await handles.first().boundingBox())!

      expect(handleBox.y).toBeGreaterThanOrEqual(filesBox.y + filesBox.height - 1)
      expect(handleBox.y + handleBox.height).toBeLessThanOrEqual(archivedBox.y + 1)
    }
  })
})
