import { expect, test } from './fixtures'

/**
 * Smoke test for sidebar section drag-drop. The drop-position math and
 * cross-sidebar move logic are exhaustively tested at the unit level in
 * `tests/unit/components/sectionDragUtils.test.ts`. This e2e exercises just
 * the UI/backend integration: real pointer events through solid-dnd, the
 * persisted MoveSection RPC, and the post-reload sidebar restore.
 */

function waitForMoveSection(page: import('@playwright/test').Page) {
  return page.waitForResponse(
    resp => resp.url().includes('SectionService/MoveSection') && resp.ok(),
  )
}

async function getSectionOrder(page: import('@playwright/test').Page, side: 'left' | 'right') {
  const root = page.locator(`[data-testid="sidebar-${side}"]`)
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

test.describe('Section Reorder & Move', () => {
  test('cross-sidebar drag persists across reload', async ({ page, authenticatedWorkspace }) => {
    // Default state: Left=[In Progress, Archived], Right=[Files]. Drag the
    // last left-side section into the right sidebar and confirm it survives
    // a page reload (backend save round-trips correctly).
    const leftBefore = await getSectionOrder(page, 'left')
    expect(leftBefore.length).toBeGreaterThanOrEqual(1)
    const source = leftBefore.at(-1)!

    const rightBefore = await getSectionOrder(page, 'right')
    expect(rightBefore.length).toBeGreaterThanOrEqual(1)
    const target = rightBefore[0]

    const sourceHandle = page.locator(`[data-testid="${source}"] [data-testid^="section-drag-handle-"]`)
    const targetSummary = page.locator(`[data-testid="${target}-summary"]`)
    await expect(sourceHandle).toBeVisible()
    await expect(targetSummary).toBeVisible()

    const sourceBox = (await sourceHandle.boundingBox())!
    const targetBox = (await targetSummary.boundingBox())!

    const saved = waitForMoveSection(page)
    await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2)
    await page.waitForTimeout(100)
    await page.mouse.down()
    await page.waitForTimeout(100)
    await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y + 5, { steps: 15 })
    await page.waitForTimeout(100)
    await page.mouse.up()
    await saved
    await page.waitForTimeout(500)

    await page.reload()
    await expect(page.locator(`[data-testid="${source}"]`)).toBeVisible()
    const rightAfter = await getSectionOrder(page, 'right')
    expect(rightAfter).toContain(source)
  })
})
