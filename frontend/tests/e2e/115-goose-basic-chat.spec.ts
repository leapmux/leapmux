import { expect, GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { lastAssistantBubble, sendMessage, waitForAgentIdle } from './helpers/ui'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest.describe('Goose Basic Chat', () => {
  gooseTest('send message and receive response', async ({ authenticatedGooseWorkspace, page }) => {
    void authenticatedGooseWorkspace
    await sendMessage(page, 'What is 2+2? Reply with just the number.')
    await waitForAgentIdle(page, 120_000)

    const bubble = await lastAssistantBubble(page)
    const text = await bubble.textContent()
    expect(text).toContain('4')
  })
})
