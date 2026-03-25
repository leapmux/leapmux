import { codexTest, expect } from './codex-fixtures'
import { openSettingsMenu, waitForSettingsIdle } from './helpers/ui'

const GPT_54_MINI_RE = /gpt-5\.4 mini/i

codexTest.describe('Codex Agent Settings', () => {
  codexTest('Codex agent uses correct default model', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace

    const settingsBtn = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(settingsBtn).toBeVisible()
    await expect(settingsBtn).toContainText(GPT_54_MINI_RE)
  })

  codexTest('Codex agent can toggle Fast mode from the settings menu', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace

    await openSettingsMenu(page)
    await expect(page.locator('[data-testid="codex-service-tier-default"] input[type="radio"]')).toBeChecked()
    await page.locator('[data-testid="codex-service-tier-fast"]').click()
    await waitForSettingsIdle(page)

    await openSettingsMenu(page)
    await expect(page.locator('[data-testid="codex-service-tier-fast"] input[type="radio"]')).toBeChecked()
  })

  codexTest('Fast mode group appears first in the settings popover', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace

    await openSettingsMenu(page)
    await expect(page.locator('[data-testid="agent-settings-menu"] fieldset legend').first()).toHaveText('Fast Mode')
  })

  codexTest('Codex agent can enter plan mode from the settings menu', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    await openSettingsMenu(page)
    await expect(page.locator('[data-testid="agent-settings-menu"]')).toContainText('Workflow')
    await expect(page.locator('[data-testid="codex-collaboration-mode-plan"]')).toBeVisible()
    await page.locator('[data-testid="codex-collaboration-mode-plan"]').click()

    await expect(trigger).toContainText('Plan Mode')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page)
    await expect(page.locator('[data-testid="codex-collaboration-mode-plan"] input[type="radio"]')).toBeChecked()
  })

  codexTest('Shift+Tab toggles sticky Codex plan mode', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()
    await expect(trigger).toContainText('Suggest & Approve')

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()

    await page.keyboard.press('Shift+Tab')
    await expect(trigger).toContainText('Plan Mode')
    await waitForSettingsIdle(page)

    await page.keyboard.press('Shift+Tab')
    await expect(trigger).toContainText('Suggest & Approve')
    await waitForSettingsIdle(page)
  })
})
