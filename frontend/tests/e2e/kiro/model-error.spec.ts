import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { expectRateLimitNotice } from '../helpers/rateLimit'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

/**
 * The words that Kiro states for a throttled model call. Kiro adds the id of the
 * request after them, which differs for each call.
 */
const KIRO_THROTTLE_TEXT = 'Too many requests, please wait before trying again.'

kiroTest.describe('Kiro basic chat', () => {
  // Kiro shows a throttle as a display error, and then fails the prompt with the
  // same words. The transcript states them, not a generic prompt failure.
  kiroTest('states the reason of a model call that the service throttled', async ({ authenticatedKiroWorkspace, page, modelScript, leapmuxServer }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
    const before = await currentNativeAgent(context)
    // Kiro sends a throttled call again by itself, and the number of tries is
    // Kiro's own, so the fallback throttles each one.
    await modelScript.fallback({ error: { status: 429, code: 'ThrottlingException', message: 'Rate exceeded' } })
    await sendMessage(page, modelScript.prompt('Say hello.'))
    await waitForAgentIdle(page)
    await expectRateLimitNotice(page, KIRO_THROTTLE_TEXT)
    expect((await modelScript.status()).requests.length, 'Kiro made the model call').toBeGreaterThan(0)
    const queue = page.locator('[data-testid="queue-pause-button"]:visible')
    if (await queue.count() > 0 && (await queue.textContent())?.includes('Resume'))
      await queue.click()
    const recovered = await sendNativeAnswer(context, 'Reply once after the native throttled turn ends.', 'The native Kiro session recovered after its service failure.')
    expect(recovered.protocol).toBe('aws-event-stream')
    expect((await currentNativeAgent(context)).id).toBe(before.id)
  })
})
