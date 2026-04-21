import { expect, test } from './fixtures'
import { lastAssistantBubble } from './helpers/ui'

/**
 * Verifies the per-agent startup queue: when the user types and sends a
 * message before the agent's subprocess has finished starting, the
 * bubble appears immediately with a "Queued" sublabel and is delivered
 * the instant the agent transitions to ACTIVE.
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

    // Type and submit while still in STARTING. The frontend should
    // intercept the send and queue rather than dispatching the RPC.
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number.')
    await page.keyboard.press('Meta+Enter')

    // Editor clears immediately on submit regardless of queueing.
    await expect(editor).toHaveText('')

    if (overlayWasVisible) {
      // While still queued, the optimistic bubble must show the
      // pending sublabel — proof the message was held back, not sent.
      const pending = page.locator('[data-testid="message-pending"]')
      await expect(pending).toBeVisible({ timeout: 5_000 })
      await expect(pending).toContainText('Queued')

      // The startup overlay disappears when the agent transitions
      // to ACTIVE. The pending sublabel disappears with it.
      await expect(overlay).not.toBeVisible({ timeout: 60_000 })
      await expect(pending).not.toBeVisible({ timeout: 30_000 })
    }

    // The queued message must reach Claude and produce a response.
    await expect(lastAssistantBubble(page)).toContainText('4', { timeout: 60_000 })
  })
})
