import { expect, fastAgentTest } from '../fastagent-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

fastAgentTest.describe('Fast Agent thinking and context usage', () => {
  fastAgentTest('reports the usage block as context usage', async ({ native, page }) => {
    await exerciseContextUsage(native)
    await expect(page.locator('[data-testid="agent-info-trigger"]:visible').getByTestId('context-usage-grid')).toBeVisible()
  })
})
