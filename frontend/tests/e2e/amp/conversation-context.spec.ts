import { expect } from '@playwright/test'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, waitForAgentIdle } from '../helpers/ui'

/**
 * A second real native request must contain the earlier prompt and answer. Each turn keeps its own completion row.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest.describe('Amp basic chat', () => {
  ampTest('keeps the conversation from one turn to the next', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
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

    // The second inference carries the first prompt and the first answer: the
    // thread holds the whole conversation.
    const second = status.requests.find(request => request.stepIndex === 1)
    expect(second).toBeDefined()
    const body = JSON.stringify(second?.body)
    expect(body).toContain('1234 + 5678')
    expect(body).toContain(ARITHMETIC_ANSWER_TEXT)
  })
})
