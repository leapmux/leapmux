import { diracTest, expect } from '../dirac-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

diracTest.describe('Dirac thinking and context usage', () => {
  diracTest('reports the usage block as context usage', async ({ native, page }) => {
    await exerciseContextUsage(native)
    await expect(page.locator('[data-testid="agent-info-trigger"]:visible').getByTestId('context-usage-grid')).toBeVisible()
  })
})
