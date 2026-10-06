import { expect, fastAgentTest } from '../fastagent-fixtures'
import { expectContextUsage } from '../helpers/contextUsage'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

fastAgentTest.describe('Fast Agent thinking and context usage', () => {
  fastAgentTest('reports the usage block as context usage', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await modelScript.queue({
      text: 'The turn is complete.',
      usage: { inputTokens: 1200, outputTokens: 80, contextWindow: 8000 },
    })
    await sendMessage(page, modelScript.prompt('Finish the turn.'))
    await waitForAgentIdle(page)

    await expect(page.locator('[data-testid="agent-info-trigger"]').getByTestId('context-usage-grid')).toBeVisible()
    await expectContextUsage(page, { inputTokens: 1200, outputTokens: 80 })
  })
})
