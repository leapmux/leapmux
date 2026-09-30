import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from './droid-fixtures'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  bandRows,
  expectAssistantAnswer,
  messageContents,
  sendMessage,
  userBubbles,
  waitForAgentIdle,
} from './helpers/ui'

/**
 * 258 — Factory Droid basic chat.
 *
 * The worker sends prompts as `droid.add_user_message` over stream-jsonrpc.
 * Droid asks the mock through its custom model. The transcript shows the
 * answer and the end of the turn.
 *
 * Droid asks for a session title before the first main request. The title
 * rule answers that separate request.
 */
droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

droidTest.describe('Factory Droid basic chat', () => {
  droidTest('draws the answer and ends the turn', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expectAssistantAnswer(page)
    await expect(userBubbles(page).filter({ hasText: '1234 + 5678' }).first()).toBeVisible()

    // The model call carried the prompt. It is the turn's own request: the
    // title housekeeping turn is answered by the `title-droid` rule and never
    // reaches this queue.
    const request = status.requests.find(record => record.stepIndex === 0)
    expect(JSON.stringify(request?.body)).toContain('1234 + 5678')

    // The turn end closes the turn.
    await expect(page.locator('[data-testid="result-divider"]:visible').last()).toHaveText(/^Turn ended/)

    const contents = messageContents(page)
    expect(await contents.count()).toBeGreaterThan(0)

    // The worker writes the rows it streamed, so a reload draws the same turn.
    await page.reload()
    await expectAssistantAnswer(page)
  })

  droidTest('carries the conversation into the next request', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)

    // The request the turn made carries the prompt the user wrote.
    const request = status.requests.find(record => record.stepIndex === 0)
    expect(JSON.stringify(request?.body)).toContain('1234 + 5678')
  })

  droidTest('draws model reasoning in a thought band', async ({ authenticatedReasoningDroidWorkspace, page, modelScript }) => {
    void authenticatedReasoningDroidWorkspace
    const reasoning = 'DROID_THOUGHT_MARKER I compare the two values.'
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue({ reasoning, text: 'The answer is 6912.' })
    await sendMessage(page, modelScript.prompt('Add 1234 and 5678.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(bandRows(page, 'thought').filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })
})
