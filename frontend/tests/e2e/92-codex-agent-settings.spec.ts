import type { Page } from '@playwright/test'
import { codexTest, expect } from './codex-fixtures'

const GPT_54_MINI_RE = /gpt-5\.4 mini/i

async function openSettingsMenu(page: Page) {
  const trigger = page.locator('[data-testid="agent-settings-trigger"]')
  const menu = page.locator('[data-testid="agent-settings-menu"]')
  await expect(trigger).toBeVisible()
  await expect(async () => {
    if (!await menu.isVisible()) {
      await trigger.click()
    }
    await expect(menu).toBeVisible()
  }).toPass({ timeout: 5000 })
}

async function waitForSettingsIdle(page: Page) {
  await expect(page.locator('[data-testid="settings-loading-spinner"]')).not.toBeVisible()
}

codexTest.describe('Codex Agent Settings', () => {
  codexTest('Codex agent uses correct default model', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace

    const settingsBtn = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(settingsBtn).toBeVisible()
    await expect(settingsBtn).toContainText(GPT_54_MINI_RE)
  })

  codexTest('Codex agent can enter plan mode from the settings menu', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    await openSettingsMenu(page)
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
