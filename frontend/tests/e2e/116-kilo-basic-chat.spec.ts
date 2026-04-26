import { lastAssistantBubble, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

kiloTest.describe('Kilo Basic Chat', () => {
  kiloTest('send message and receive response', async ({ authenticatedKiloWorkspace, page }) => {
    void authenticatedKiloWorkspace
    await sendMessage(page, 'What is 2+2? Reply with just the number.')
    await waitForAgentIdle(page, 120_000)

    const bubble = await lastAssistantBubble(page)
    const text = await bubble.textContent()
    expect(text).toContain('4')
  })
})
