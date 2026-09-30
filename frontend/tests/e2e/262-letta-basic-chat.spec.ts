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
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from './letta-fixtures'

/**
 * 262 — Letta Code basic chat.
 *
 * The worker sends each prompt over Letta's protocol_v2 WebSocket. Letta asks
 * the mock through its `openai-compatible` provider. The transcript shows the
 * model answer and the turn end.
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

  lettaTest('draws model reasoning in a thought band', async ({ authenticatedReasoningLettaWorkspace, page, modelScript }) => {
    void authenticatedReasoningLettaWorkspace
    const reasoning = 'LETTA_THOUGHT_MARKER I compare the two values.'
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue({ reasoning, text: 'The answer is 6912.' })
    await sendMessage(page, modelScript.prompt('Add 1234 and 5678.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(bandRows(page, 'thought').filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })
})
