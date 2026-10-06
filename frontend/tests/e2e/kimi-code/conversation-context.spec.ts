import { expect } from '@playwright/test'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, expectAssistantAnswer, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('uses Kimi Code for basic chat', () => {
  // Each prompt goes to the same kap-server session, so the second request
  // carries the first exchange.
  kimiTest('keeps one session across two turns', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)

    await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    const second = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(second?.body)).toContain(ARITHMETIC_PROMPT)
    expect(JSON.stringify(second?.body)).toContain(ARITHMETIC_ANSWER_TEXT)
    await expect(assistantBubbles(page).filter({ hasText: ARITHMETIC_ANSWER_TEXT })).not.toHaveCount(0)
  })
})
