import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'

const REASONING = 'I add the two numbers.'

codewhaleTest.describe('Codewhale basic chat', () => {
  codewhaleTest('answers a message and keeps its thinking after a reload', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    // The `deepseek` route reads `reasoning_content` as thinking, so the
    // reasoning reaches the transcript as its own row rather than as answer text.
    await modelScript.queue({ reasoning: REASONING, text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const expectSeparateRows = async () => {
      await expectAssistantAnswer(page)
      await expect(bandRows(page, 'thought').filter({ hasText: REASONING }).first()).toBeVisible()
      // The answer row holds the answer alone. A route that is not known to
      // reason merges the reasoning into the answer text instead.
      await expect(bandRows(page, 'text').filter({ hasText: ARITHMETIC_ANSWER_TEXT }).filter({ hasText: REASONING })).toHaveCount(0)
    }
    await expectSeparateRows()

    await page.reload()
    await expectSeparateRows()
  })
})
