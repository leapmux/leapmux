import { DIRAC_E2E_SKIP_REASON, diracTest, expect } from './dirac-fixtures'
import { diracRespondToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

/**
 * A Dirac turn ends ONLY when the model calls `respond` with
 * `operation: "complete"`. A text-only reply loops the turn until timeout, so
 * every scripted turn queues a `respond` tool call.
 */
diracTest.describe('Dirac Basic Chat', () => {
  diracTest('send message and receive response', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-respond', 'complete', 'Hello from the mock model.')] })
    await sendMessage(page, modelScript.prompt('Say hello.'))
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'Hello from the mock model.' }).first()).toBeVisible()
  })
})
