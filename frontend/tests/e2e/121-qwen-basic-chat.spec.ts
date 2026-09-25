import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'
import { QWEN_E2E_SKIP_REASON, qwenTest } from './qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('send message and receive response', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
  })
})
