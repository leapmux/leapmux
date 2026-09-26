import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest } from './fastagent-fixtures'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

/**
 * fast-agent serves OpenAI Chat Completions with `stream: true` AND
 * `stream: false` on one turn (two calls). The mock answers both from one
 * scripted step.
 */
fastAgentTest.describe('Fast Agent Basic Chat', () => {
  fastAgentTest('send message and receive response', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await modelScript.queue({ text: 'Hello from the mock model.' })
    await sendMessage(page, modelScript.prompt('Say hello.'))
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'Hello from the mock model.' }).first()).toBeVisible()
  })
})
