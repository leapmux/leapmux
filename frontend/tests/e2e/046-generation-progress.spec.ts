import { codexTest, expect } from './codex-fixtures'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

const OUTPUT_MARKER = 'LEAPMUX OUTPUT two'
const INTERRUPTION_MARKER = 'Text truncated by interruption.'

codexTest.describe('generation progress', () => {
  codexTest('shows process bytes without rendering partial process output', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace
    await page.evaluate((marker) => {
      const state = window as Window & {
        __generationOutputSeen?: boolean
        __generationPartialOutputSeen?: boolean
      }
      state.__generationOutputSeen = false
      state.__generationPartialOutputSeen = false
      const inspect = () => {
        const indicator = document.querySelector<HTMLElement>('[data-testid="thinking-indicator"]')
        const outputVisible = indicator !== null && /≥?\d+(?:\.\d+)?\s+(?:B|KB|MB)/.test(indicator.textContent ?? '')
        if (!outputVisible)
          return
        state.__generationOutputSeen = true
        const toolRows = Array.from(document.querySelectorAll<HTMLElement>('[data-tool-message]'))
        if (toolRows.some(row => row.textContent?.includes(marker)))
          state.__generationPartialOutputSeen = true
      }
      const observer = new MutationObserver(inspect)
      observer.observe(document.body, { childList: true, subtree: true, characterData: true })
      inspect()
    }, OUTPUT_MARKER)

    await sendMessage(page, 'This is a protocol test. Run this exact harmless command once and wait for it to finish: for word in one two three four five six seven eight; do python3 -c \'import math; math.factorial(300000)\'; echo LEAPMUX OUTPUT $word; done. Do not use another tool. Then report that the command finished.')

    await expect.poll(async () => page.evaluate(() =>
      (window as Window & { __generationOutputSeen?: boolean }).__generationOutputSeen), { timeout: 120_000 }).toBe(true)
    expect(await page.evaluate(() =>
      (window as Window & { __generationPartialOutputSeen?: boolean }).__generationPartialOutputSeen)).toBe(false)

    await waitForAgentIdle(page, 120_000)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: OUTPUT_MARKER }).first()).toBeVisible()
    await page.reload()
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: OUTPUT_MARKER }).first()).toBeVisible()
  })

  codexTest('keeps interrupted model text and its marker after reload', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace
    await sendMessage(page, 'Write a detailed 3000-word explanation of sorting algorithms. Use no tools and start the answer immediately.')

    const indicator = page.locator('[data-testid="thinking-indicator"]:visible')
    await expect(indicator).toContainText('tokens', { timeout: 120_000 })
    await page.locator('[data-testid="interrupt-button"]:visible').click()
    await waitForAgentIdle(page, 120_000)

    await expect(assistantBubbles(page).filter({ hasText: INTERRUPTION_MARKER }).first()).toBeVisible()
    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: INTERRUPTION_MARKER }).first()).toBeVisible()
  })
})
