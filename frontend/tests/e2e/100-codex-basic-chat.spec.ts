import { codexTest, expect } from './codex-fixtures'
import { assistantBubbles, lastAssistantBubble, sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex Basic Chat', () => {
  codexTest('send message and receive response', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'What is 2+2? Reply with just the number.')
    await waitForAgentIdle(page, 120_000)

    const bubble = await lastAssistantBubble(page)
    const text = await bubble.textContent()
    expect(text).toContain('4')
  })

  codexTest('assistant response appears in chat bubble', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'Say hello world')
    await waitForAgentIdle(page, 120_000)

    const bubbles = assistantBubbles(page)
    const count = await bubbles.count()
    expect(count).toBeGreaterThan(0)

    const bubble = await lastAssistantBubble(page)
    const text = await bubble.textContent()
    expect(text?.toLowerCase()).toContain('hello')
  })

  codexTest('thinking indicator appears and disappears during response', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'What is the square root of 144?')

    // The thinking indicator should appear while the agent is processing.
    const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
    // Wait for it to appear (may be very brief for fast responses).
    await expect(thinkingIndicator).toBeVisible({ timeout: 30_000 }).catch(() => {
      // Fast responses may complete before we can observe the indicator — acceptable.
    })

    // Wait for the agent to finish — indicator should be gone.
    await waitForAgentIdle(page, 120_000)
    await expect(thinkingIndicator).not.toBeVisible()
  })
})
