import { codexTest, expect } from './codex-fixtures'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex context usage', () => {
  // The usage block the mock reports is the only source of these counts. A
  // default of 1/1 would make every number equal; 12000/40 is the marker.
  codexTest('the agent info grid follows the usage the model reports', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await modelScript.queue({
      text: 'Usage recorded.',
      usage: { inputTokens: 12000, outputTokens: 40 },
    })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const infoTrigger = page.locator('[data-testid="agent-info-trigger"]')
    await expect(infoTrigger).toBeVisible()
    await infoTrigger.click()
    const popover = page.locator('[data-testid="agent-info-popover"]')
    await expect(popover).toBeVisible()
    const grid = popover.getByTestId('context-usage-grid')
    await expect(grid).toBeVisible()
    await expect(grid).toContainText('12')
    await expect(grid).toContainText('40')
  })
})
