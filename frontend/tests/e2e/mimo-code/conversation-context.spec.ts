import { expect } from '@playwright/test'
import { escapeRegExp } from '../../../src/lib/regexp'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code basic chat', () => {
  // The second prompt reaches the same MiMo session.
  // The model reads the first exchange from that session.
  mimoTest('continues the same session on a second prompt', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)

    await modelScript.rule({
      name: 'the second turn carries the first exchange',
      when: { body: [escapeRegExp(ARITHMETIC_PROMPT), ARITHMETIC_ANSWER_TEXT, '1111 \\+ 2222'] },
      respond: { text: SECOND_ARITHMETIC_ANSWER_TEXT },
      once: true,
    })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
    await expect(userBubbles(page)).toHaveCount(2)
    const second = (await modelScript.status()).requests.find(request => request.rule === 'the second turn carries the first exchange')
    if (!second)
      throw new Error('The second native request did not reach the exact conversation rule.')
    expect(JSON.stringify(second.body)).toContain(ARITHMETIC_PROMPT)
    expect(JSON.stringify(second.body)).toContain(ARITHMETIC_ANSWER_TEXT)
  })
})
