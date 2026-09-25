import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest, expect } from './codewhale-fixtures'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, bandRows, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

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

  codewhaleTest('continues the same thread with a second message', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await modelScript.queue({ text: 'The code word is HALIBUT.' })
    await sendMessage(page, modelScript.prompt('Remember a code word for me.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await modelScript.queue({ text: 'You asked me to remember HALIBUT.' })
    await sendMessage(page, modelScript.prompt('Which code word did you give me?'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The second request carries the first answer, which proves that the second
    // message joined the thread of the first rather than opening a new one.
    const { requests } = await modelScript.status()
    expect(requests).toHaveLength(2)
    expect(JSON.stringify(requests[1]!.body)).toContain('The code word is HALIBUT.')
    await expect(assistantBubbles(page).filter({ hasText: 'You asked me to remember HALIBUT.' })).toBeVisible()
  })
})
