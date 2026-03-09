import { expect, test } from './fixtures'

test.describe('Resizable Sidebars', () => {
  test('should render left and right panels', async ({ page, authenticatedWorkspace }) => {
    // Left sidebar should be visible (use section header testid to avoid ambiguity with breadcrumbs)
    await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()

    // Right sidebar should be visible with "Files" header
    await expect(page.locator('[data-testid="section-header-files-summary"]')).toBeVisible()
  })

  test('should show resize handles between panels', async ({ page, authenticatedWorkspace }) => {
    // Resize handles are button elements with the ResizeablePanelGroup-ResizeHandle class
    const handles = page.locator('[data-testid="resize-handle"]')
    // Should have 2 handles: left|center and center|right
    await expect(handles).toHaveCount(2)
  })

  test('should resize left panel via drag', async ({ page, authenticatedWorkspace }) => {
    // Get the first resize handle (left|center)
    const handle = page.locator('[data-testid="resize-handle"]').first()
    await expect(handle).toBeVisible()

    // Let the layout fully settle before measuring.
    // The sidebar and file tree may still be loading/rendering after workspace creation.
    await page.waitForTimeout(1000)

    // Record initial position of the handle
    const boxBefore = (await handle.boundingBox())!

    // Drag the handle 150px to the right with deliberate pauses for reliable detection.
    // The resize library listens for mousedown on the handle, then mousemove/mouseup
    // on the document. Small delays ensure each phase is properly registered.
    const startX = boxBefore.x + boxBefore.width / 2
    const startY = boxBefore.y + boxBefore.height / 2
    await page.mouse.move(startX, startY)
    await page.waitForTimeout(100)
    await page.mouse.down()
    await page.waitForTimeout(100)
    await page.mouse.move(startX + 150, startY, { steps: 30 })
    await page.waitForTimeout(100)
    await page.mouse.up()

    // Handle should have moved to the right (left sidebar got wider)
    const boxAfter = (await handle.boundingBox())!
    expect(boxAfter.x).toBeGreaterThan(boxBefore.x)
  })
})

test.describe('Pane Resize Handles', () => {
  /**
   * Helper to ensure only In Progress and Archived sections are expanded,
   * so that exactly one pane resize handle appears between them.
   * Collapses any other open sections (e.g., Workers) first.
   */
  async function ensureOnlyTwoSectionsExpanded(page: any) {
    const inProgress = page.locator('[data-testid="section-header-workspaces_in_progress"]')
    const archived = page.locator('[data-testid="section-header-workspaces_archived"]')

    await expect(inProgress).toBeVisible()
    await expect(archived).toBeVisible()

    // Collapse any other open sections (Workers, etc.) to isolate the test
    const allSections = page.locator('[data-testid^="section-header-"]:not([data-testid$="-summary"])')
    const count = await allSections.count()
    for (let i = 0; i < count; i++) {
      const section = allSections.nth(i)
      const testId = await section.getAttribute('data-testid')
      if (testId === 'section-header-workspaces_in_progress' || testId === 'section-header-workspaces_archived')
        continue
      const isOpen = await section.evaluate((el: HTMLDetailsElement) => el.open)
      if (isOpen)
        await section.locator('> summary').click()
    }

    // Ensure In Progress is expanded (it should be by default, but check)
    const inProgressIsOpen = await inProgress.evaluate((el: HTMLDetailsElement) => el.open)
    if (!inProgressIsOpen)
      await inProgress.locator('> summary').click()

    // Ensure Archived is expanded
    const archivedIsOpen = await archived.evaluate((el: HTMLDetailsElement) => el.open)
    if (!archivedIsOpen)
      await archived.locator('> summary').click()

    // Wait for exactly one resize handle between the two sections
    await expect(page.locator('[data-testid="pane-resize-handle"]')).toHaveCount(1)
  }

  test('should show pane resize handle between expanded left sidebar sections', async ({ page, authenticatedWorkspace }) => {
    await ensureOnlyTwoSectionsExpanded(page)

    // Verify the pane resize handle is between the two sections
    const paneHandles = page.locator('[data-testid="pane-resize-handle"]')
    await expect(paneHandles).toHaveCount(1)
  })

  test('should resize left sidebar panes via drag', async ({ page, authenticatedWorkspace }) => {
    await ensureOnlyTwoSectionsExpanded(page)

    // Let layout settle
    await page.waitForTimeout(500)

    const handle = page.locator('[data-testid="pane-resize-handle"]').first()
    await expect(handle).toBeVisible()

    // Get initial position
    const boxBefore = (await handle.boundingBox())!

    // Drag the handle 80px downward (vertical resize between panes)
    const startX = boxBefore.x + boxBefore.width / 2
    const startY = boxBefore.y + boxBefore.height / 2
    await page.mouse.move(startX, startY)
    await page.waitForTimeout(100)
    await page.mouse.down()
    await page.waitForTimeout(100)
    await page.mouse.move(startX, startY + 80, { steps: 20 })
    await page.waitForTimeout(100)
    await page.mouse.up()

    // Handle should have moved downward
    const boxAfter = (await handle.boundingBox())!
    expect(boxAfter.y).toBeGreaterThan(boxBefore.y)
  })

  test('should hide pane resize handle when only one section is open', async ({ page, authenticatedWorkspace }) => {
    await ensureOnlyTwoSectionsExpanded(page)

    // Collapse Archived section
    await page.locator('[data-testid="section-header-workspaces_archived"] > summary').click()

    // Pane resize handle should disappear
    await expect(page.locator('[data-testid="pane-resize-handle"]')).toHaveCount(0)
  })

  test('should reset pane sizes on double-click', async ({ page, authenticatedWorkspace }) => {
    await ensureOnlyTwoSectionsExpanded(page)

    // Let layout settle
    await page.waitForTimeout(500)

    const handle = page.locator('[data-testid="pane-resize-handle"]').first()
    await expect(handle).toBeVisible()

    // First drag the handle down to make sections unequal
    const boxBefore = (await handle.boundingBox())!
    const startX = boxBefore.x + boxBefore.width / 2
    const startY = boxBefore.y + boxBefore.height / 2
    await page.mouse.move(startX, startY)
    await page.waitForTimeout(100)
    await page.mouse.down()
    await page.waitForTimeout(100)
    await page.mouse.move(startX, startY + 100, { steps: 20 })
    await page.waitForTimeout(100)
    await page.mouse.up()

    const boxAfterDrag = (await handle.boundingBox())!
    expect(boxAfterDrag.y).toBeGreaterThan(boxBefore.y)

    // Double-click the handle to reset
    await handle.dblclick()
    await page.waitForTimeout(300)

    // Handle should have moved back toward the center (closer to original position)
    const boxAfterReset = (await handle.boundingBox())!
    // The reset position should be closer to the original than the dragged position
    const distFromOriginalAfterDrag = Math.abs(boxAfterDrag.y - boxBefore.y)
    const distFromOriginalAfterReset = Math.abs(boxAfterReset.y - boxBefore.y)
    expect(distFromOriginalAfterReset).toBeLessThan(distFromOriginalAfterDrag)
  })

  test('should not show pane resize handle above user menu', async ({ page, authenticatedWorkspace }) => {
    // The user menu is a railOnly section at the bottom — it should never
    // get a resize handle. Verify by ensuring only In Progress is open and
    // confirming no pane handles exist despite user menu being present.

    await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()

    // Collapse all sections except In Progress
    const allSections = page.locator('[data-testid^="section-header-"]:not([data-testid$="-summary"])')
    const count = await allSections.count()
    for (let i = 0; i < count; i++) {
      const section = allSections.nth(i)
      const testId = await section.getAttribute('data-testid')
      if (testId === 'section-header-workspaces_in_progress')
        continue
      const isOpen = await section.evaluate((el: HTMLDetailsElement) => el.open)
      if (isOpen)
        await section.locator('> summary').click()
    }
    await page.waitForTimeout(200)

    // With only In Progress open, there should be no pane handles
    await expect(page.locator('[data-testid="pane-resize-handle"]')).toHaveCount(0)

    // The user menu section should be visible at the bottom
    await expect(page.locator('[data-testid="user-menu-trigger"]')).toBeVisible()
  })
})
