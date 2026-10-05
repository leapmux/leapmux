import { DIRAC_E2E_SKIP_REASON, diracTest, expect } from '../dirac-fixtures'
import { expectContextUsage } from '../helpers/contextUsage'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

diracTest.describe('Dirac thinking and context usage', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

  diracTest('reports the usage block as context usage', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    const usage = { inputTokens: 1200, outputTokens: 80, contextWindow: 8000 }
    await modelScript.queue({
      usage,
      toolCalls: [diracRespondToolCall('dirac-usage', 'complete', 'The turn is complete.')],
    })
    await sendMessage(page, modelScript.prompt('Finish the turn.'))
    await waitForAgentIdle(page)

    const infoTrigger = page.locator('[data-testid="agent-info-trigger"]')
    await expect(infoTrigger.getByTestId('context-usage-grid')).toBeVisible()
    await expectContextUsage(page, usage)
  })
})
