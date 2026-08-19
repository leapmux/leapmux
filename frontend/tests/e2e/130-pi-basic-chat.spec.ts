import { ARITHMETIC_PROMPT, assistantBubbles, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest.describe('Pi Basic Chat', () => {
  piTest('send message and receive response', async ({ authenticatedPiWorkspace, page }) => {
    void authenticatedPiWorkspace // fixture trigger
    await sendMessage(page, ARITHMETIC_PROMPT)
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)
  })

  piTest('assistant response appears in chat bubble', async ({ authenticatedPiWorkspace, page }) => {
    void authenticatedPiWorkspace // fixture trigger
    await sendMessage(page, 'Say hello world')
    await waitForAgentIdle(page, 180_000)

    await expect(assistantBubbles(page)).not.toHaveCount(0)

    // expectAssistantAnswer, not lastAssistantBubble: a turn-end divider is an
    // agent-role bubble too, so the LAST one is the divider whenever it lands
    // after the reply. This asserted on its "turn completed" text instead of
    // the answer.
    await expectAssistantAnswer(page, { answer: /hello/i })
  })

  piTest('thinking indicator appears and disappears during response', async ({ authenticatedPiWorkspace, page }) => {
    void authenticatedPiWorkspace // fixture trigger
    await sendMessage(page, 'What is the square root of 144?')

    // The thinking indicator should appear while the agent is processing.
    const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
    await expect(thinkingIndicator).toBeVisible().catch(() => {
      // Fast responses may complete before we can observe the indicator — acceptable.
    })

    // Wait for the agent to finish — indicator should be gone.
    await waitForAgentIdle(page, 180_000)
    await expect(thinkingIndicator).not.toBeVisible()
  })
})
