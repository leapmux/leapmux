import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { clearGoal, expectEmptyGoalCard, expectGoalObjective, expectGoalStatus, submitGoal } from '../helpers/goalsAndTodos'
import { exerciseLiveChildTranscript } from '../helpers/liveChildTranscript'
import { lastUserText, scenarioUserTexts } from '../helpers/mockModelScript'
import { exerciseNativeCodeExecution, nativeJavaScriptTextCases } from '../helpers/nativeCodeExecution'
import { expectNoUnrenderedRow, sendNativeAnswer } from '../helpers/nativeConversation'
import { waitForNativeInputQueueIdle } from '../helpers/nativeInputQueueIdle'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { assistantBubbles, messageContents, sendMessage, visibleOnly, waitForAgentIdle } from '../helpers/ui'

/** The gate that holds the buffered request until this test releases it. */
const BUFFERED_GATE = 'buffered'

/** The buffering notice offers this model. Its native catalog supports code-mode tools. */
const FASTER_MODEL = 'gpt-5.6-sol'

/** Codex's notice row states the buffering reason. The banner uses neutral text. */
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
 * `response.created` and before the hold. Codex reads the safety notice while the step waits for the test to release its hold.
 * The moderation entry exercises the frame that
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
      await expect(visibleOnly(page.getByTestId('turn-retry-offer-banner'))).not.toContainText(BUFFERING_NOTICE)

      // The worker replays its current offer without another model event.
      await page.reload()
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

  codexTest('retries the buffered request and preserves raw events on the same native thread', async ({ native }) => {
    const before = await currentNativeAgent(native)
    await sendNativeAnswer(native, 'Keep REVERT_PREFIX in the conversation.', 'REVERT_PREFIX retained.')
    const retried = await retryBufferedTurn(native)
    const summaries = (await native.modelScript.status()).requests.filter(request => request.rule === 'retry-model-compaction')
    const retained = summaries[0] ?? retried
    expect(scenarioUserTexts(retained.protocol, retained.body).filter(text => text.includes('REVERT_PREFIX'))).toHaveLength(1)
    if (summaries.length > 0)
      expect(scenarioUserTexts(retained.protocol, retained.body).filter(text => text.includes(PROMPT))).toHaveLength(0)
    await exerciseNativeCodeExecution(native, { scripts: nativeJavaScriptTextCases })
    const after = await currentNativeAgent(native)
    expect(after.agentSessionId).toBe(before.agentSessionId)
    await exerciseLiveChildTranscript(native, {
      childWhen: { body: ['NEW_TASK', 'answer_the_live_child_task'] },
      childTask: 'Report the child prompt after the retry.',
      parentTask: 'Spawn a child after the retry.',
    })
  })

  codexTest('retries retained input before an active goal can continue automatically', async ({ native }) => {
    await retryBufferedTurn(native, {
      beforeRetry: async () => {
        await submitGoal(native.page, native.modelScript.prompt('Keep the retry goal active.'))
        await expectGoalStatus(native.page, 'active')
      },
      afterRetryStarts: async () => {
        await expectGoalObjective(native.page, 'Keep the retry goal active.')
        await expectGoalStatus(native.page, 'active')
        await clearGoal(native.page)
        // Codex emits the removal notification only when a native goal exists.
        await expectEmptyGoalCard(native.page)
      },
    })
  })

  codexTest('keeps an existing child route through the parent retry', async ({ native }) => {
    const child = await exerciseLiveChildTranscript(native, {
      childWhen: { body: ['NEW_TASK', 'answer_the_live_child_task'] },
      childTask: 'Finish the existing child after the parent retries.',
      parentTask: 'Start a child before the parent retries.',
      beforeRelease: async () => { await retryBufferedTurn(native) },
    })
    await expect(child.row).toHaveAttribute('data-status', 'succeeded')
  })
})

/** Retry one held native turn and verify the input that Codex receives after revert. */
async function retryBufferedTurn(native: ManagedNativeScenarioContext, hooks: {
  beforeRetry?: () => Promise<void>
  afterRetryStarts?: () => Promise<void>
} = {}) {
  const { page, modelScript } = native
  const agent = await currentNativeAgent(native)
  const fastGate = 'retry-fast-answer'
  // A model switch can compact the retained prefix before it submits the retried input.
  await modelScript.rule({
    name: 'retry-model-compaction',
    priority: 'high',
    when: { user: ['CONTEXT CHECKPOINT COMPACTION', 'Create a handoff summary'] },
    respond: { text: 'The earlier work is complete. Continue with the next user request.' },
  })
  const start = await modelScript.queue(
    { responseMetadata: BUFFERED_METADATA, gate: BUFFERED_GATE, text: 'The slow answer.' },
    { gate: fastGate, text: 'The fast answer.' },
  )
  try {
    await sendMessage(page, modelScript.prompt(PROMPT))
    await modelScript.waitForGate(BUFFERED_GATE)
    await hooks.beforeRetry?.()
    await expect(visibleOnly(page.getByTestId('turn-retry-offer-banner'))).toHaveCount(1)
    await visibleOnly(page.getByTestId('turn-retry-offer-retry')).click()
    await page.getByTestId('turn-retry-confirm-dialog').getByRole('button', { name: 'Stop and retry' }).click()
    await modelScript.waitForGate(fastGate)
    const retried = await modelScript.requestAt(start + 1)
    expect(retried.body).toMatchObject({ model: FASTER_MODEL, reasoning: { effort: 'low' } })
    expect(lastUserText(retried.protocol, retried.body)).toContain(PROMPT)
    expect(scenarioUserTexts(retried.protocol, retried.body).filter(text => text.includes(PROMPT))).toHaveLength(1)
    await hooks.afterRetryStarts?.()
    await modelScript.releaseGate(fastGate)
    await modelScript.waitForSteps(start + 2)
    await waitForNativeInputQueueIdle(native.leapmuxServer, agent.id, modelScript.testDeadline)
    await expect(visibleOnly(page.getByTestId('result-divider')).filter({ hasText: 'Turn interrupted' }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'Stopped this attempt and retried on' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The fast answer.' }).first()).toBeVisible()
    await expect(page.getByTestId('turn-retry-offer-banner')).toHaveCount(0)
    await expectNoUnrenderedRow(page)
    return retried
  }
  finally {
    await modelScript.releaseGateIfHeld(BUFFERED_GATE)
    await modelScript.releaseGateIfHeld(fastGate)
  }
}

for (const model of [FASTER_MODEL, 'a-model-with-a-long-unbroken-identifier-that-exceeds-the-composer-width']) {
  codexTest(`keeps the retry controls inside a narrow composer for ${model}`, async ({ native }) => {
    const { page, modelScript } = native
    await page.setViewportSize({ width: 1200, height: 800 })
    await page.addStyleTag({ content: '[data-testid="agent-editor-panel"] { max-width: 260px; }' })
    const step = await modelScript.queue({
      responseMetadata: [{ ...BUFFERED_METADATA[0], retry_model: model }],
      gate: BUFFERED_GATE,
      text: 'The answer after the wait.',
    })
    try {
      await sendMessage(page, modelScript.prompt(PROMPT))
      await modelScript.waitForGate(BUFFERED_GATE)
      const banner = visibleOnly(page.getByTestId('turn-retry-offer-banner'))
      await expect(banner).toBeVisible()
      const geometry = await banner.evaluate((element) => {
        const area = element.getBoundingClientRect()
        const sentence = element.querySelector('span')!.getBoundingClientRect()
        const controls = Array.from(element.querySelectorAll('button, a'), control => control.getBoundingClientRect())
        return {
          width: area.width,
          overflow: element.scrollWidth - element.clientWidth,
          sentenceWidth: sentence.width,
          outside: controls.some(control => control.left < area.left - 1 || control.right > area.right + 1),
          overlap: controls.some(control => control.left < sentence.right && control.right > sentence.left
            && control.top < sentence.bottom && control.bottom > sentence.top),
        }
      })
      expect(geometry.width).toBeLessThanOrEqual(260)
      expect(geometry.sentenceWidth).toBeGreaterThan(80)
      expect(geometry.overflow).toBeLessThanOrEqual(1)
      expect(geometry.outside).toBe(false)
      expect(geometry.overlap).toBe(false)
      await expect(banner.getByRole('link', { name: 'Learn more' })).toBeVisible()
      await visibleOnly(page.getByTestId('turn-retry-offer-retry')).click()
      const dialog = page.getByTestId('turn-retry-confirm-dialog')
      await expect(dialog).toBeVisible()
      await dialog.getByRole('button', { name: 'Keep waiting' }).click()
      await expect(banner).toHaveCount(0)
    }
    finally {
      await modelScript.releaseGateIfHeld(BUFFERED_GATE)
    }
    await modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(page)
  })
}
