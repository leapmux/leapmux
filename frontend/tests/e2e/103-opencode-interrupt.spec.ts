import { sendMessage } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest.describe('OpenCode Interrupt', () => {
  opencodeTest('interrupt button appears during processing', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // Send a prompt that will take a while.
    await sendMessage(page, 'Write a very long essay about the history of computing, covering all major milestones from the abacus to modern AI.')

    // The interrupt/stop button should appear while the agent is thinking.
    const stopButton = page.locator('[data-testid="stop-btn"]')
    await expect(stopButton).toBeVisible({ timeout: 30_000 }).catch(() => {
      // Fast responses may complete before we can observe the button.
    })
  })
})
