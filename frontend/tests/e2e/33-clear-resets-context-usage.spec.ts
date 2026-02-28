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

    // Record the pre-clear context percentage from the grid's aria-label.
    const getContextPercentage = () => contextGrid.evaluate((svg) => {
      const label = svg.getAttribute('aria-label')
      const match = label?.match(/(\d+)%/)
      return match ? Number(match[1]) : null
    })
    const percentBefore = await getContextPercentage()
    expect(percentBefore).toBeGreaterThan(0)

    // Click the trigger to open the popover and verify it shows
    // the structured context usage info with the "Context" label.
    await infoTrigger.click()
    const popover = page.locator('[data-testid="session-id-popover"]')
    await expect(popover).toBeVisible()
    await expect(popover.getByText('Context')).toBeVisible()
    // Dismiss the popover with Escape (clicking the trigger may be
    // intercepted by the popover overlay).
    await page.keyboard.press('Escape')
    await expect(popover).not.toBeVisible()

    // Send /clear to reset the session
    await editor.click()
    await page.keyboard.type('/clear')
    await page.keyboard.press('Meta+Enter')

    // Wait for the "Context cleared" notification — this confirms the
    // backend processed the clear and context_cleared event was received.
    await expect(page.getByText('Context cleared')).toBeVisible()

    // After /clear, the store clears contextUsage (grid briefly hidden),
    // but the agent immediately sends a fresh agent_session_info with the
    // new session's base context (system prompt), so the grid reappears
    // with a much lower percentage. Verify the percentage dropped.
    await expect(infoTrigger).toBeVisible()
    await expect(async () => {
      const count = await contextGrid.count()
      if (count === 0)
        return // Grid gone — clear worked
      const pct = await getContextPercentage()
      if (pct === null)
        return // No percentage — clear worked
      expect(pct).toBeLessThan(percentBefore!)
    }).toPass({ timeout: 15_000 })
  })
})
