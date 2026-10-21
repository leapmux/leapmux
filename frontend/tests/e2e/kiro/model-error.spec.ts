import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectRateLimitNotice } from '../helpers/rateLimit'
import { messageContents, resumeQueueAfterFailure, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'

/**
 * The words that Kiro states for a throttled model call. Kiro adds the id of the
 * request after them, which differs for each call.
 */
const KIRO_THROTTLE_TEXT = 'Too many requests, please wait before trying again.'

kiroTest.describe('Kiro basic chat', () => {
  // Kiro shows a throttle as a display error, and then fails the prompt with the
  // same words. The transcript states them, not a generic prompt failure.
  kiroTest('states the reason of a model call that the service throttled', async ({ native, page, modelScript }) => {
    const before = await currentNativeAgent(native)
    // Kiro sends a throttled call again by itself, and the number of tries is
    // Kiro's own, so the fallback throttles each one.
    await modelScript.fallback({ error: { status: 429, code: 'ThrottlingException', message: 'Rate exceeded' } })
    await sendMessage(page, modelScript.prompt('Say hello.'))
    await waitForAgentIdle(page)
    await expectRateLimitNotice(page, KIRO_THROTTLE_TEXT)
    expect((await modelScript.status()).requests.length, 'Kiro made the model call').toBeGreaterThan(0)
    // Kiro fails the prompt and ends its turn, and its process keeps running, so the queue keeps running.
    await resumeQueueAfterFailure(page, 'running')
    const recovered = await sendNativeAnswer(native, 'Reply once after the native throttled turn ends.', 'The native Kiro session recovered after its service failure.')
    expect(recovered.protocol).toBe('aws-event-stream')
    expect((await currentNativeAgent(native)).id).toBe(before.id)
  })

  // A content-filter refusal rides its own chunk: the answer keeps its text and
  // the refusal states its category, explanation and recommended model.
  kiroTest('states a content-filter refusal beside the answer text', async ({ native, modelScript }) => {
    const start = await modelScript.queue({
      text: 'Probe refusal explanation.',
      stopReason: 'refusal',
    })
    await sendMessage(native.page, modelScript.prompt('Say the refused answer.'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(native.page)
    await expect(messageContents(native.page).filter({ hasText: 'The model refused this request (CYBER)' })).toHaveCount(1)
    await expect(messageContents(native.page).filter({ hasText: 'Recommended model: kiro-probe-alt' })).toHaveCount(1)
  })
})
