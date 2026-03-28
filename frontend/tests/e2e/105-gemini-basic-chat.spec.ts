import { expect, GEMINI_E2E_SKIP_REASON, geminiTest } from './gemini-fixtures'
import { lastAssistantBubble, sendMessage, waitForAgentIdle } from './helpers/ui'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest.describe('Gemini Basic Chat', () => {
  geminiTest('send message and receive response', async ({ authenticatedGeminiWorkspace, page }) => {
    void authenticatedGeminiWorkspace
    await sendMessage(page, 'What is 2+2? Reply with just the number.')
    await waitForAgentIdle(page, 120_000)

    const bubble = await lastAssistantBubble(page)
    const text = await bubble.textContent()
    expect(text).toContain('4')
  })
})
