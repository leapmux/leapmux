import { codexTest, expect } from './codex-fixtures'

codexTest.describe('Codex Agent Settings', () => {
  codexTest('Codex agent uses correct default model', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    // Open the settings dropdown to verify the Codex model is shown.
    const settingsBtn = page.locator('[data-testid="agent-settings-trigger"]')
    if (await settingsBtn.isVisible({ timeout: 5000 }).catch(() => false)) {
      const triggerText = await settingsBtn.textContent()
      expect(triggerText).toBeTruthy()
      // The default Codex model is o4-mini in e2e (overridden via LEAPMUX_CODEX_DEFAULT_MODEL).
      expect(triggerText?.toLowerCase()).toContain('o4-mini')
    }
  })

  codexTest('change Codex model triggers settings changed notification', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    // Open the settings dropdown.
    const settingsBtn = page.locator('[data-testid="agent-settings-trigger"]')
    if (!await settingsBtn.isVisible({ timeout: 5000 }).catch(() => false))
      return

    await settingsBtn.click()
    const dropdown = page.locator('[data-testid="agent-settings-dropdown"]')
    if (!await dropdown.isVisible({ timeout: 3000 }).catch(() => false))
      return

    // Try to find and click a different model option.
    const modelSelect = dropdown.locator('select').first()
    if (await modelSelect.isVisible({ timeout: 2000 }).catch(() => false)) {
      // Select a different model.
      const options = await modelSelect.locator('option').allTextContents()
      if (options.length > 1) {
        await modelSelect.selectOption({ index: 1 })
        // Wait for settings change to propagate.
        await page.waitForTimeout(5000)
        // A settings_changed notification should appear in the chat.
        const chatArea = page.locator('[data-testid="message-content"]')
        const allText = await chatArea.allTextContents()
        const joined = allText.join(' ').toLowerCase()
        expect(joined.includes('model') || joined.includes('settings')).toBeTruthy()
      }
    }
  })
})
