import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest } from '../fastagent-fixtures'
import { expectContextUsage } from '../helpers/contextUsage'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

fastAgentTest.describe('Fast Agent thinking and context usage', () => {
  fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

  fastAgentTest('reports the usage block as context usage', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await modelScript.queue({
      text: 'The turn is complete.',
      usage: { inputTokens: 1200, outputTokens: 80, contextWindow: 8000 },
    })
    await sendMessage(page, modelScript.prompt('Finish the turn.'))
    await waitForAgentIdle(page, 120_000)

    await expect(page.locator('[data-testid="agent-info-trigger"]').getByTestId('context-usage-grid')).toBeVisible()
    await expectContextUsage(page, { inputTokens: 1200, outputTokens: 80 })
  })
})
