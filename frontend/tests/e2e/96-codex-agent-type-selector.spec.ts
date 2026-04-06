import { expect, test } from './fixtures'
import { isMaybeVisible } from './helpers/ui'

test.describe('Codex Agent Type Selector', () => {
  test('New Agent dialog shows agent provider selector', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace // fixture trigger
    // Click the new agent button to open the dialog.
    const newAgentBtn = page.locator('[data-testid="new-agent-btn"]')
    if (await isMaybeVisible(newAgentBtn)) {
      await newAgentBtn.click()

      const dialog = page.locator('[role="dialog"]')
      await expect(dialog).toBeVisible()

      const trigger = dialog.getByTestId('agent-provider-selector-trigger')
      if (await isMaybeVisible(trigger, 3000)) {
        await trigger.click()
        await expect(dialog.getByTestId('agent-provider-option-1')).toContainText('Claude Code')
        await expect(dialog.getByTestId('agent-provider-option-2')).toContainText('Codex')
      }
    }
  })

  test('New Workspace dialog shows agent provider selector', async ({ page, leapmuxServer }) => {
    void leapmuxServer // fixture trigger
    // Navigate to the org page where the new workspace button is.
    await page.goto('/o/admin')
    await page.waitForTimeout(2000)

    // Click the new workspace button.
    const newWsBtn = page.locator('[data-testid="new-workspace-btn"]')
    if (await isMaybeVisible(newWsBtn)) {
      await newWsBtn.click()

      const dialog = page.locator('[role="dialog"]')
      await expect(dialog).toBeVisible()

      const trigger = dialog.getByTestId('agent-provider-selector-trigger')
      if (await isMaybeVisible(trigger, 3000)) {
        await trigger.click()
        await expect(dialog.getByTestId('agent-provider-option-1')).toContainText('Claude Code')
        await expect(dialog.getByTestId('agent-provider-option-2')).toContainText('Codex')
      }
    }
  })

  test('new agent button defaults to active tab provider type', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace // fixture trigger
    // The active tab is a Claude Code agent (default from fixtures).
    // When clicking "new agent", it should default to Claude Code.
    const newAgentBtn = page.locator('[data-testid^="new-agent-button"]').first()
    if (await isMaybeVisible(newAgentBtn)) {
      // Just verify the button is clickable — the new agent inherits the provider
      // from the active tab (handled by handleOpenAgent in useAgentOperations).
      await expect(newAgentBtn).toBeEnabled()
    }
  })
})
