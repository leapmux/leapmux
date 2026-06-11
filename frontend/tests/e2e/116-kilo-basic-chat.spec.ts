import { ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'
import { KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

kiloTest.describe('Kilo Basic Chat', () => {
  kiloTest('send message and receive response', async ({ authenticatedKiloWorkspace, page }) => {
    void authenticatedKiloWorkspace
    await sendMessage(page, ARITHMETIC_PROMPT)
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
  })
})
