import { expect } from '@playwright/test'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, bandRows, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

/** The thinking that the first turn scripts. */
const REASONING = 'The sum is small.'

kiroTest.describe('Kiro basic chat', () => {
  kiroTest('sends a message and receives the response', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await modelScript.queue({ reasoning: REASONING, text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)
    // The thinking is a row of its own, and the answer row holds the answer alone.
    await expect(bandRows(page, 'thought').filter({ hasText: REASONING }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: ARITHMETIC_ANSWER_TEXT }).filter({ hasText: REASONING })).toHaveCount(0)
  })
})
