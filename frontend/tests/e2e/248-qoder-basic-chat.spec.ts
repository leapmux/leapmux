import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  bandRows,
  expectAssistantAnswer,
  sendMessage,
  visibleOnly,
  waitForAgentIdle,
} from './helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

/**
 * 248 — Qoder CLI basic chat.
 *
 * The worker sends each prompt as a Qoder stream-json `user` frame. Qoder asks
 * the mock through its custom provider. Its `result` frame ends the turn.
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
    await expect(visibleOnly(page.getByText('LeapMux has no display for this row', { exact: true }))).toHaveCount(0)

    // The worker writes the rows it streamed, so a reload draws the same turn.
    await page.reload()
    await expectAssistantAnswer(page)
    await expect(visibleOnly(page.getByText('LeapMux has no display for this row', { exact: true }))).toHaveCount(0)
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

  qoderTest('draws model reasoning in a thought band', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    const reasoning = 'QODER_THOUGHT_MARKER I compare the two values.'
    await modelScript.queue({ reasoning, text: 'The answer is 6912.' })
    await sendMessage(page, modelScript.prompt('Add 1234 and 5678.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(bandRows(page, 'thought').filter({ hasText: reasoning }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: reasoning })).toHaveCount(0)
  })
})
