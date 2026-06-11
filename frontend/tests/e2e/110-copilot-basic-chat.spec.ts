import { COPILOT_E2E_SKIP_REASON, copilotTest } from './copilot-fixtures'
import { ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest.describe('Copilot Basic Chat', () => {
  copilotTest('send message and receive response', async ({ authenticatedCopilotWorkspace, page }) => {
    void authenticatedCopilotWorkspace
    await sendMessage(page, ARITHMETIC_PROMPT)
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
  })
})
