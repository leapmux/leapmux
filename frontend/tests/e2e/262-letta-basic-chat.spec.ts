import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  expectAssistantAnswer,
  messageContents,
  sendMessage,
  userBubbles,
  waitForAgentIdle,
} from './helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from './letta-fixtures'

/**
 * 262 — Letta Code basic chat.
 *
 * The worker starts one `letta server --listen` App Server for the agent and
 * sends each prompt as a `create_message` input over the protocol_v2 WebSocket.
 * Letta asks the mock through its `openai-compatible` provider. One scripted
 * turn proves that a prompt reaches the model and that the answer reaches the
 * chat, and that the turn end closes the turn.
 */
lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

lettaTest.describe('Letta Code basic chat', () => {
  lettaTest('draws the answer and ends the turn', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expectAssistantAnswer(page)
    await expect(userBubbles(page).filter({ hasText: '1234 + 5678' }).first()).toBeVisible()

    // The model call carried the prompt the user wrote.
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

  lettaTest('carries the conversation into the request', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)
    await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)

    const request = status.requests.find(record => record.stepIndex === 0)
    expect(JSON.stringify(request?.body)).toContain('1234 + 5678')
  })
})
