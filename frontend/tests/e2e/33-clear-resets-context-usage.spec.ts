import { expect, test } from './fixtures'

test.describe('Clear Command – Context Usage Reset', () => {
  test('context usage indicator resets after /clear', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message to establish a session and populate context info
    // (branch, cwd, version, cost, contextUsage).
    await editor.click()
    await page.keyboard.type('What is 1+1? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('2') && !body.includes('Send a message to start')
    })

    // Wait for the ContextUsageGrid to show the 3x3 SVG grid (non-zero
    // context usage), confirming the agent has sent context info.
    const infoTrigger = page.locator('[data-testid="session-id-trigger"]')
    const contextGrid = infoTrigger.locator('svg[viewBox="0 0 11 11"]')
    await expect(contextGrid).toBeVisible({ timeout: 60_000 })

    // Verify the grid currently has at least one filled (active) square
    // by checking that not all rects share the same computed fill color.
    // Active squares use currentColor (or the warning variable) while
    // inactive squares use --context-grid-inactive.
    const getDistinctFillColors = () => contextGrid.evaluate((svg) => {
      const rects = svg.querySelectorAll('rect')
      const colors = new Set<string>()
      for (const r of rects) {
        colors.add(window.getComputedStyle(r).fill)
      }
      return [...colors]
    })
    const colorsBefore = await getDistinctFillColors()
    expect(colorsBefore.length).toBeGreaterThan(1)

    // Click the trigger to open the popover and verify it shows
    // the structured context usage info with the "Context" label.
    await infoTrigger.click()
    const popover = page.locator('[data-testid="session-id-popover"]')
    await expect(popover).toBeVisible()
    await expect(popover.getByText('Context')).toBeVisible()
    // Click trigger again to close the popover.
    await infoTrigger.click()
    await expect(popover).not.toBeVisible()

    // Send /clear to reset the session
    await editor.click()
    await page.keyboard.type('/clear')
    await page.keyboard.press('Meta+Enter')

    // Wait for the "Context cleared" notification — this confirms the
    // backend processed the clear and context_cleared event was received.
    await expect(page.getByText('Context cleared')).toBeVisible()

    // After /clear, context usage is cleared. The ContextUsageGrid replaces
    // the 3x3 grid with an Info icon fallback when contextUsage is undefined.
    // The info trigger itself remains visible (session is still active).
    await expect(infoTrigger).toBeVisible()
    await expect(contextGrid).not.toBeVisible({ timeout: 10_000 })
  })
})
