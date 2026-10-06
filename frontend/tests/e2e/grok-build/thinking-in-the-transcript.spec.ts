import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('draws model reasoning in a thought row', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await modelScript.queue({ reasoning: 'I inspect the numbers first.', text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(bandRows(page, 'thought').filter({ hasText: 'I inspect the numbers first.' }).first()).toBeVisible()
    await expectAssistantAnswer(page)
  })
})
