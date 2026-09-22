import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'
import { KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

kiloTest.describe('Kilo Basic Chat', () => {
  kiloTest('send message and receive response', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
    void authenticatedKiloWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
  })
})
