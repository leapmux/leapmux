import { GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest.describe('Goose Basic Chat', () => {
  gooseTest('send message and receive response', async ({ authenticatedGooseWorkspace, page }) => {
    void authenticatedGooseWorkspace
    await sendMessage(page, ARITHMETIC_PROMPT)
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
  })
})
