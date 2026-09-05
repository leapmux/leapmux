import { expect, test } from './fixtures'
import { ARITHMETIC_PROMPT, expectAssistantAnswer } from './helpers/ui'

/**
 * Verifies that the Worker queue persists input while an agent starts.
 */
test.describe('Claude Code agent startup queue', () => {
  test('queues a typed-during-startup message and delivers it on ACTIVE', async ({ page, authenticatedWorkspace }) => {
    // Editor is reachable while the agent is still STARTING — the new
    // OpenAgent flow returns immediately and renders the loader overlay.
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // The startup overlay must be visible at the start (or transition
    // through it). Don't fail if the agent already finished — capture
    // both legal trajectories.
    const overlay = page.locator('[data-testid="agent-startup-overlay"]')
    const overlayWasVisible = await overlay.isVisible().catch(() => false)

    // Type and submit while the agent starts.
    await editor.click()
    await page.keyboard.type(ARITHMETIC_PROMPT)
    await page.keyboard.press('Meta+Enter')

    // The editor clears after the Worker accepts the queue item.
    await expect(editor).toHaveText('')

    if (overlayWasVisible) {
      const queue = page.getByTestId('agent-input-queue')
      await expect(queue).toContainText(ARITHMETIC_PROMPT)
      await expect(overlay).not.toBeVisible()
      await expect(queue).toHaveCount(0)
    }

    // The queued message must reach Claude and produce a response.
    await expectAssistantAnswer(page)
  })
})
