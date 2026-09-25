import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, expectAssistantAnswer, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'
import { expect, MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code basic chat', () => {
  // One scripted turn proves the whole path: the worker starts `mimo serve`,
  // prompts it over HTTP, reads the answer off the event stream, and ends the
  // turn on the idle status.
  mimoTest('renders the reasoning and the answer, then clears the thinking indicator', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue({ reasoning: 'Add the two numbers.', text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Add the two numbers.' })).toBeVisible()
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
  })

  // The second prompt reaches the same MiMo session, so the model sees the first
  // exchange in its history.
  mimoTest('continues the same session on a second prompt', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page)

    await modelScript.rule({
      name: 'the second turn carries the first exchange',
      when: { body: [ARITHMETIC_ANSWER_TEXT, '1111 \\+ 2222'] },
      respond: { text: SECOND_ARITHMETIC_ANSWER_TEXT },
      once: true,
    })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    await waitForAgentIdle(page, 120_000)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
    await expect(userBubbles(page)).toHaveCount(2)
  })
})
