import { expect } from '@playwright/test'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, expectAssistantAnswer, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, waitForAgentIdle } from '../helpers/ui'

/**
 * A second real native request must contain the earlier prompt and answer. Each turn keeps its own completion row.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest.describe('Cline basic chat', () => {
  clineTest('keeps the conversation from one turn to the next', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
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

    // The second model call carries the first prompt and the first answer: the
    // session holds the whole conversation.
    const second = status.requests.find(request => request.stepIndex === 1)
    const body = JSON.stringify(second?.body)
    expect(body).toContain('1234 + 5678')
    expect(body).toContain(ARITHMETIC_ANSWER_TEXT)
    await expect(assistantBubbles(page).filter({ hasText: ARITHMETIC_ANSWER_TEXT })).not.toHaveCount(0)
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
  })
})
