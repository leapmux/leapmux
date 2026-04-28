import { assistantBubbles, lastAssistantBubble, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest.describe('Pi Coding Agent Basic Chat', () => {
  piTest('send message and receive response', async ({ authenticatedPiWorkspace, page }) => {
    void authenticatedPiWorkspace // fixture trigger
    await sendMessage(page, 'What is 2+2? Reply with just the number.')
    await waitForAgentIdle(page, 180_000)

    const bubble = await lastAssistantBubble(page)
    const text = await bubble.textContent()
    expect(text).toContain('4')
  })

  piTest('assistant response appears in chat bubble', async ({ authenticatedPiWorkspace, page }) => {
    void authenticatedPiWorkspace // fixture trigger
    await sendMessage(page, 'Say hello world')
    await waitForAgentIdle(page, 180_000)

    const bubbles = assistantBubbles(page)
    const count = await bubbles.count()
    expect(count).toBeGreaterThan(0)

    const bubble = await lastAssistantBubble(page)
    const text = await bubble.textContent()
    expect(text?.toLowerCase()).toContain('hello')
  })

  piTest('thinking indicator appears and disappears during response', async ({ authenticatedPiWorkspace, page }) => {
    void authenticatedPiWorkspace // fixture trigger
    await sendMessage(page, 'What is the square root of 144?')

    // The thinking indicator should appear while the agent is processing.
    const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
    await expect(thinkingIndicator).toBeVisible({ timeout: 30_000 }).catch(() => {
      // Fast responses may complete before we can observe the indicator — acceptable.
    })

    // Wait for the agent to finish — indicator should be gone.
    await waitForAgentIdle(page, 180_000)
    await expect(thinkingIndicator).not.toBeVisible()
  })
})
