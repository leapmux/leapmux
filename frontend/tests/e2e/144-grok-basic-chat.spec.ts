import { GROK_E2E_SKIP_REASON, grokTest } from './grok-fixtures'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('send message and receive response', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
  })
})
