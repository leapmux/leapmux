import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  expectAssistantAnswer,
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

/**
 * 248 — Qoder CLI basic chat.
 *
 * The worker runs `qodercli --config-dir <dir> -p --input-format stream-json
 * --output-format stream-json` for the agent and sends each prompt as one stdin
 * `user` frame. Qoder asks the mock through its custom provider. One scripted
 * turn proves that a prompt reaches the model, that the answer reaches the chat,
 * and that the `result` frame closes the turn. A second turn proves that the
 * process keeps the conversation.
 */
qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

qoderTest.describe('Qoder CLI basic chat', () => {
  qoderTest('draws the answer and ends the turn', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expectAssistantAnswer(page)
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()

    // The worker writes the rows it streamed, so a reload draws the same turn.
    await page.reload()
    await expectAssistantAnswer(page)
  })

  qoderTest('keeps the conversation from one turn to the next', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)

    await modelScript.queue({ text: 'The second answer.' })
    await sendMessage(page, modelScript.prompt('And the second question?'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)
  })
})
