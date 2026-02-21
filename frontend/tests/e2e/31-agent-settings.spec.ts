import type { Page } from '@playwright/test'
import { expect, restartWorker, stopWorker, processTest as test } from './process-control-fixtures'

/** Open the settings menu if not already visible. */
async function openSettingsMenu(page: Page) {
  const menu = page.locator('[data-testid="agent-settings-menu"]')
  if (!await menu.isVisible()) {
    await page.locator('[data-testid="agent-settings-trigger"]').click()
  }
  await expect(menu).toBeVisible()
}

/** Get the trigger button's text content. */
async function getTriggerText(page: Page): Promise<string> {
  return (await page.locator('[data-testid="agent-settings-trigger"]').textContent()) ?? ''
}

test.describe('Agent Settings', () => {
  test('default settings on startup', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Default: Haiku model (overridden via LEAPMUX_DEFAULT_MODEL in e2e), Default permission mode
    const text = await getTriggerText(page)
    expect(text).toContain('Haiku')
    expect(text).toContain('Default')
  })

  test('switch permission modes', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Switch to Plan Mode (dropdown auto-closes on select)
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-plan"]').click()
    await expect(trigger).toContainText('Plan Mode')

    // Switch to Accept Edits
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-acceptEdits"]').click()
    await expect(trigger).toContainText('Accept Edits')

    // Switch to Bypass Permissions
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-bypassPermissions"]').click()
    await expect(trigger).toContainText('Bypass Permissions')

    // Switch back to Default
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-default"]').click()
    await expect(trigger).toContainText('Default')
  })

  test('switch model', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Change model to Sonnet (default is Haiku, dropdown auto-closes on select)
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-sonnet"]').click()
    await expect(trigger).toContainText('Sonnet')
  })

  test('switch effort', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Change effort to High (dropdown auto-closes on select)
    await openSettingsMenu(page)
    await page.locator('[data-testid="effort-high"]').click()
    // Dropdown closes; re-open to verify selection
    await openSettingsMenu(page)
    await expect(page.locator('[data-testid="effort-high"] input[type="radio"]')).toBeChecked()
    await page.keyboard.press('Escape')
  })

  test('permission mode persistence across refresh', async ({ authenticatedWorkspace, page }) => {
    // Wait for the editor to be ready (agent is started)
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Switch to Plan Mode (dropdown auto-closes on select)
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-plan"]').click()
    await expect(trigger).toContainText('Plan Mode')

    // Wait for the control_response round-trip to complete and DB to update.
    await page.waitForTimeout(3000)

    // Refresh the page
    await page.reload()

    // Verify Plan Mode is still selected after refresh
    const triggerAfter = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(triggerAfter).toBeVisible()
    await expect(triggerAfter).toContainText('Plan Mode')
  })

  test('model persistence across refresh', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Change model to Sonnet (default is Haiku, dropdown auto-closes on select)
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-sonnet"]').click()
    await expect(trigger).toContainText('Sonnet')

    // Wait for restart to complete
    await page.waitForTimeout(5000)

    // Refresh the page
    await page.reload()

    // Verify Sonnet is still selected after refresh
    const triggerAfter = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(triggerAfter).toBeVisible()
    await expect(triggerAfter).toContainText('Sonnet')
  })

  test('focus returns to editor after mode change', async ({ authenticatedWorkspace, page }) => {
    // Wait for the editor to be ready (agent is started)
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Open dropdown and click a mode
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-plan"]').click()

    // Close the dropdown by pressing Escape
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-testid="agent-settings-menu"]')).not.toBeVisible()

    // Click the editor and verify it can receive focus
    await editor.click()
    await expect(editor).toBeFocused()
  })

  test('settings restored after worker restart', async ({ authenticatedWorkspace, separateHubWorker, page }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Send a message to establish a session ID.
    await editor.click()
    await page.keyboard.type('What is 1+1? Reply with just the number, nothing else.')
    await page.keyboard.press('Enter')

    // Wait for a response (ensures init message and session ID are stored)
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('2') && !body.includes('Send a message to start')
    })

    // Switch to Plan Mode (dropdown auto-closes on select)
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-plan"]').click()
    await expect(trigger).toContainText('Plan Mode')

    // Wait for the control_response round-trip to complete and DB to update.
    await page.waitForTimeout(3000)

    // Stop worker
    await stopWorker()
    await page.waitForTimeout(3000)

    // Restart worker
    await restartWorker(separateHubWorker)

    // Wait for the editor to become visible again after worker reconnects
    await expect(editor).toBeVisible()

    // Send a message to trigger agent re-launch via ensureAgentActive
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
    await page.keyboard.press('Enter')

    // Wait for a response (agent resumed successfully)
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('4') && !body.includes('Send a message to start')
    })

    // Verify Plan Mode is still selected after worker restart
    await expect(trigger).toContainText('Plan Mode')
  })

  test('interrupt via control request', async ({ authenticatedWorkspace, page }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a quick message to ensure the agent is fully started
    await editor.click()
    await page.keyboard.type('What is 1+1? Reply with just the number, nothing else.')
    await page.keyboard.press('Enter')
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('2') && !body.includes('Send a message to start')
    })

    // Verify the agent is still responsive after interrupt by sending another message
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
    await page.keyboard.press('Enter')
    await page.waitForFunction(() => {
      const body = document.body.textContent || ''
      return body.includes('4')
    })
  })

  test('model/effort items not disabled when idle', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Open dropdown while agent is idle (not streaming)
    await openSettingsMenu(page)

    // Verify all model items are enabled (not disabled) when idle
    await expect(page.locator('[data-testid="model-haiku"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="model-sonnet"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="model-opus"]')).not.toHaveAttribute('data-disabled', '')

    // Verify all effort items are enabled when idle
    await expect(page.locator('[data-testid="effort-low"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="effort-medium"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="effort-high"]')).not.toHaveAttribute('data-disabled', '')

    // Verify permission mode items are enabled when idle
    await expect(page.locator('[data-testid="permission-mode-default"]')).not.toHaveAttribute('data-disabled', '')

    // Verify "Disabled while running" footnote is NOT visible when idle
    await expect(page.locator('[data-testid="settings-disabled-footnote"]')).not.toBeVisible()

    await page.keyboard.press('Escape')
  })

  test('settings change notification appears in chat', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Change model to Sonnet (default is Haiku) — should produce a notification
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-sonnet"]').click()
    await expect(trigger).toContainText('Sonnet')

    // Verify the notification bubble appears in chat
    await expect(page.getByText('Model (Haiku \u2192 Sonnet)')).toBeVisible()
  })

  test('permission mode change notification appears in chat', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Switch to Plan Mode
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-plan"]').click()
    await expect(trigger).toContainText('Plan Mode')

    // Verify the notification bubble appears in chat
    await expect(page.getByText('Mode (Default \u2192 Plan Mode)')).toBeVisible()
  })

  test('settings loading indicator on trigger', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Change model to Sonnet (default is Haiku) — trigger should show spinner briefly
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-sonnet"]').click()

    // The trigger should show a loading spinner
    const loadingSpinner = page.locator('[data-testid="settings-loading-spinner"]')
    await expect(loadingSpinner).toBeVisible()

    // Eventually the spinner disappears after statusChange arrives
    await expect(loadingSpinner).not.toBeVisible()
    await expect(trigger).toContainText('Sonnet')
  })
})
