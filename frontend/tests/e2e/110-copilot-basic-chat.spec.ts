import { COPILOT_E2E_SKIP_REASON, copilotTest, expect } from './copilot-fixtures'
import { lastAssistantBubble, sendMessage, waitForAgentIdle } from './helpers/ui'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest.describe('Copilot Basic Chat', () => {
  copilotTest('send message and receive response', async ({ authenticatedCopilotWorkspace, page }) => {
    void authenticatedCopilotWorkspace
    await sendMessage(page, 'What is 2+2? Reply with just the number.')
    await waitForAgentIdle(page, 120_000)

    const bubble = await lastAssistantBubble(page)
    const text = await bubble.textContent()
    expect(text).toContain('4')
  })
})
