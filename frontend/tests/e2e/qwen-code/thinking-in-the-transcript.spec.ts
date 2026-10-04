import { expect } from '@playwright/test'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('draws model reasoning in a thought row', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await modelScript.queue({ reasoning: 'I inspect the numbers first.', text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(bandRows(page, 'thought').filter({ hasText: 'I inspect the numbers first.' }).first()).toBeVisible()
    await expectAssistantAnswer(page)
  })
})
