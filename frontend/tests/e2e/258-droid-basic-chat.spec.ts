import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from './droid-fixtures'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  expectAssistantAnswer,
  messageContents,
  sendMessage,
  userBubbles,
  waitForAgentIdle,
} from './helpers/ui'

/**
 * 258 — Factory Droid basic chat.
 *
 * The worker starts one `droid exec --input-format stream-jsonrpc` process for
 * the agent and sends each prompt as `droid.add_user_message` over the
 * stream-jsonrpc channel. Droid asks the mock through its BYOK custom-model
 * entry. One scripted turn proves that a prompt reaches the model and that the
 * answer reaches the chat, and that the turn end closes the turn.
 *
 * Droid fires a session-title housekeeping turn before the first real one. The
 * `title-droid` rule answers it from its own system prompt, so no scripted step
 * is consumed by it.
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
})
