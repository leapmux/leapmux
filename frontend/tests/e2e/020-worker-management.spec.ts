import { expect } from '@playwright/test'
import { test } from './fixtures'
import { expandSidebarSection, expectClipsLongText, expectClipsToOneLine, sidebarSectionHeader } from './helpers/ui'

test.describe('Worker Management', () => {
  test('should show Workers section with registered worker', async ({ page, authenticatedWorkspace }) => {
    // Workers section should be visible in the sidebar, and open
    const workersSection = sidebarSectionHeader(page, 'workers')
    await expandSidebarSection(workersSection)

    // Should contain the worker named "Local" (dev mode sets LEAPMUX_WORKER_NAME=Local)
    await expect(workersSection.getByTestId('worker-name').filter({ hasText: 'Local' })).toBeVisible()
  })

  test('should show green status dot for online worker', async ({ page, authenticatedWorkspace }) => {
    const workersSection = sidebarSectionHeader(page, 'workers')
    await expandSidebarSection(workersSection)

    // The status dot should indicate "connected"
    await expect(workersSection.locator('[data-status="connected"]')).toBeVisible()
  })

  /**
   * A worker row clips its name rather than widening the list.
   *
   * The list used to render into the workspace list's container, which sizes to
   * its widest row so a deep path can be scrolled into view. A worker row has
   * no such depth, and that width made the ellipsis unreachable: a long name
   * scrolled the whole sidebar section sideways instead of clipping.
   *
   * The dev worker is called `Local`, which fits any sidebar width, and the
   * name already declared the whole clipping quartet before the container was
   * fixed. So the declarations pass on the broken code too, and only a name
   * longer than its box tells the two containers apart --
   * `expectClipsLongText` supplies one.
   */
  test('clips the worker name and never scrolls the section sideways', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const workersSection = sidebarSectionHeader(page, 'workers')
    await expandSidebarSection(workersSection)

    const name = workersSection.getByTestId('worker-name').filter({ hasText: 'Local' })
    await expect(name).toBeVisible()
    await expectClipsToOneLine(name)
    await expectClipsLongText(name)
  })

  test('vertically centers the status dot with the worker name and menu trigger', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const workersSection = sidebarSectionHeader(page, 'workers')
    await expandSidebarSection(workersSection)

    const row = workersSection.getByTestId('worker-row').filter({ hasText: 'Local' })
    const name = row.getByTestId('worker-name')
    const dot = row.locator('[data-status]')
    const trigger = row.locator('button[aria-expanded]')
    await expect(dot).toBeVisible()

    const [nameBox, dotBox, triggerBox] = await Promise.all([
      name.boundingBox(),
      dot.boundingBox(),
      trigger.boundingBox(),
    ])
    expect(nameBox).not.toBeNull()
    expect(dotBox).not.toBeNull()
    expect(triggerBox).not.toBeNull()

    const centerY = (box: { y: number, height: number }) => box.y + box.height / 2
    const centerX = (box: { x: number, width: number }) => box.x + box.width / 2

    // The status dot and the kebab trigger must occupy the same action-slot cell
    // rather than stacking into two grid rows when `<ot-dropdown>` and `<Tooltip>`
    // use `display: contents`.
    expect(Math.abs(centerY(dotBox!) - centerY(triggerBox!))).toBeLessThanOrEqual(1)
    expect(Math.abs(centerX(dotBox!) - centerX(triggerBox!))).toBeLessThanOrEqual(1)
    expect(Math.abs(centerY(dotBox!) - centerY(nameBox!))).toBeLessThanOrEqual(2)

    const restingRowBox = await row.boundingBox()
    expect(restingRowBox).not.toBeNull()

    // Hovering swaps the dot for the kebab trigger in place without shifting row size.
    await row.hover()
    await expect(trigger).toHaveCSS('opacity', '1')
    await expect(dot).toHaveCSS('opacity', '0')
    const hoveredRowBox = await row.boundingBox()
    expect(hoveredRowBox).not.toBeNull()
    expect(Math.abs(hoveredRowBox!.height - restingRowBox!.height)).toBeLessThanOrEqual(1)
    expect(Math.abs(hoveredRowBox!.width - restingRowBox!.width)).toBeLessThanOrEqual(1)
  })
})
