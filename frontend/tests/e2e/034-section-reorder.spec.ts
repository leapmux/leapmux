import { expect } from '@playwright/test'
import { test } from './fixtures'
import { boxCenter, mouseDragOnto } from './helpers/drag'
import { boxOf } from './helpers/ui'

/**
 * Smoke test for sidebar section drag-drop. The drop-position math and
 * cross-sidebar move logic are exhaustively tested at the unit level in
 * `src/components/shell/sectionDragUtils.test.ts`. This e2e exercises just
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

    const targetBox = await boxOf(targetSummary)

    const saved = waitForMoveSection(page)
    // Onto the top edge of the target section's summary row, so the section
    // lands before it.
    await mouseDragOnto(page, {
      from: await boxCenter(sourceHandle),
      to: { x: targetBox.x + targetBox.width / 2, y: targetBox.y + 5 },
      steps: 15,
    })
    // The hub answered MoveSection, so the reload below reads the stored order.
    await saved

    await page.reload()
    await expect(page.locator(`[data-testid="${source}"]`)).toBeVisible()
    const rightAfter = await getSectionOrder(page, 'right')
    expect(rightAfter).toContain(source)
  })
})
