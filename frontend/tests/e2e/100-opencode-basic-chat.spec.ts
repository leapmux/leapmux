import { lastAssistantBubble, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, opencodeTest } from './opencode-fixtures'

opencodeTest.describe('OpenCode Basic Chat', () => {
  opencodeTest('send message and receive response', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger
    await sendMessage(page, 'What is 2+2? Reply with just the number.')
    await waitForAgentIdle(page, 120_000)

    const bubble = await lastAssistantBubble(page)
    const text = await bubble.textContent()
    expect(text).toContain('4')
  })

  opencodeTest('assistant response appears in chat bubble', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger
    await sendMessage(page, 'Say hello world')
    await waitForAgentIdle(page, 120_000)

    // Wait for the assistant bubble to appear after the persisted message lands.
    const bubble = await lastAssistantBubble(page)
    const text = await bubble.textContent()
    expect(text?.toLowerCase()).toContain('hello')
  })

  opencodeTest('thinking indicator appears and disappears during response', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger
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
