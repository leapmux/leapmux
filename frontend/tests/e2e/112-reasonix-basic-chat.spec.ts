import { lastAssistantBubble, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest.describe('Reasonix Basic Chat', () => {
  reasonixTest('send message and receive response', async ({ authenticatedReasonixWorkspace, page }) => {
    void authenticatedReasonixWorkspace
    await sendMessage(page, 'What is 2+2? Reply with just the number.')
    await waitForAgentIdle(page, 120_000)

    const bubble = await lastAssistantBubble(page)
    const text = await bubble.textContent()
    expect(text).toContain('4')
  })
})
