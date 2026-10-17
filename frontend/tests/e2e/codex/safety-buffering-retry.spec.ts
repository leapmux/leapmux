import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { lastUserText, scenarioUserTexts } from '../helpers/mockModelScript'
import { expectNoUnrenderedRow } from '../helpers/nativeConversation'
import { assistantBubbles, messageContents, sendMessage, visibleOnly, waitForAgentIdle } from '../helpers/ui'

/** The gate that holds the buffered request until this test releases it. */
const BUFFERED_GATE = 'buffered'

/** The faster model that the buffering notice offers. The mock does not validate model IDs. */
const FASTER_MODEL = 'gpt-5.5'

/** The notice row and banner sentence that state the buffering. */
const BUFFERING_NOTICE = 'Giving this request a little extra thought'

/**
 * The prompt that buffers. The retry test compares the request text with this
 * sentence alone, not with the scripted prompt: the composer can change the
 * whitespace before the scenario marker.
 */
const PROMPT = 'Explain the safety review process.'

/**
 * The `response.metadata` entries of one buffered request.
 *
 * The mock writes one `response.metadata` event per entry, right after
 * `response.created` and before the hold. Codex reads the safety notice there
 * while the gated step still runs. The moderation entry exercises the frame that
 * the `initialize` opt-out hides from the transcript.
 */
const BUFFERED_METADATA: Record<string, unknown>[] = [
  { type: 'safety_buffering', use_cases: ['cyber'], reasons: ['probes'], retry_model: FASTER_MODEL },
  { openai_chatgpt_moderation_metadata: { generation: {}, prompt: {}, tool_call: {}, tool_response: {} } },
]

codexTest.describe('offers a faster retry while a request buffers', () => {
  codexTest('keeps the buffered request waiting when the reader keeps waiting', async ({ native }) => {
    const { page, modelScript } = native
    const stepIndex = await modelScript.queue({ responseMetadata: BUFFERED_METADATA, gate: BUFFERED_GATE, text: 'The slow answer.' })
    try {
      await sendMessage(page, modelScript.prompt(PROMPT))
      await modelScript.waitForGate(BUFFERED_GATE)

      // The buffering states its notice in the transcript and its offer in the composer.
      await expect(messageContents(page).filter({ hasText: BUFFERING_NOTICE }).first()).toBeVisible()
      await expect(visibleOnly(page.getByTestId('turn-retry-offer-banner'))).toHaveCount(1)

      await visibleOnly(page.getByTestId('turn-retry-offer-keep-waiting')).click()
      await expect(page.getByTestId('turn-retry-offer-banner')).toHaveCount(0)

      await modelScript.releaseGate(BUFFERED_GATE)
      await modelScript.waitForSteps(stepIndex + 1)
      await waitForAgentIdle(page)

      await expect(assistantBubbles(page).filter({ hasText: 'The slow answer.' }).first()).toBeVisible()
      // The moderation metadata draws no row, which the opt-out guarantees.
      await expectNoUnrenderedRow(page)
    }
    finally {
      await modelScript.releaseGateIfHeld(BUFFERED_GATE)
    }
  })

  codexTest('retries the buffered request on the faster model when the reader asks', async ({ native }) => {
    const { page, modelScript } = native
    const start = await modelScript.queue(
      { responseMetadata: BUFFERED_METADATA, gate: BUFFERED_GATE, text: 'The slow answer.' },
      { text: 'The fast answer.' },
    )
    try {
      await sendMessage(page, modelScript.prompt(PROMPT))
      await modelScript.waitForGate(BUFFERED_GATE)

      await expect(visibleOnly(page.getByTestId('turn-retry-offer-banner'))).toHaveCount(1)
      await visibleOnly(page.getByTestId('turn-retry-offer-retry')).click()
      await page.getByTestId('turn-retry-confirm-dialog').getByRole('button', { name: 'Stop and retry' }).click()

      // The retry sends the retained prompt again on the forked thread. The
      // second queued answer is the answer of the faster model.
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)

      const retried = await modelScript.requestAt(start + 1)
      expect(retried.body).toMatchObject({ model: FASTER_MODEL, reasoning: { effort: 'low' } })
      // The retry sends the original prompt again. The fork excludes the
      // stopped attempt, so exactly one user row of the request holds the
      // prompt: a second row would be the prompt of the stopped attempt.
      expect(lastUserText(retried.protocol, retried.body)).toContain(PROMPT)
      expect(scenarioUserTexts(retried.protocol, retried.body).filter(text => text.includes(PROMPT))).toHaveLength(1)

      await expect(visibleOnly(page.getByTestId('result-divider')).filter({ hasText: 'Turn interrupted' }).first()).toBeVisible()
      await expect(messageContents(page).filter({ hasText: 'Stopped this attempt and retried on' }).first()).toBeVisible()
      await expect(assistantBubbles(page).filter({ hasText: 'The fast answer.' }).first()).toBeVisible()
      await expect(page.getByTestId('turn-retry-offer-banner')).toHaveCount(0)
      await expectNoUnrenderedRow(page)
    }
    finally {
      await modelScript.releaseGateIfHeld(BUFFERED_GATE)
    }
  })
})
