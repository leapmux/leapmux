import type { Page } from '@playwright/test'
import { ASSISTANT_BUBBLE_SELECTOR, lastAssistantBubble, openAgentViaUI, openSettingsMenu, waitForSettingsIdle } from './helpers/ui'
import { expect, restartWorker, stopWorker, processTest as test } from './process-control-fixtures'

/** Get the trigger button's text content. */
async function getTriggerText(page: Page): Promise<string> {
  return (await page.locator('[data-testid="agent-settings-trigger"]').textContent()) ?? ''
}

test.describe('Agent Settings', () => {
  test('default settings on startup', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Default: Sonnet model (overridden via LEAPMUX_CLAUDE_DEFAULT_MODEL in e2e), Default permission mode
    const text = await getTriggerText(page)
    expect(text).toContain('Sonnet')
    expect(text).toContain('Default')
  })

  test('switch permission modes', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Switch to Plan Mode (dropdown auto-closes on select)
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-plan"]').click()
    await expect(trigger).toContainText('Plan Mode')
    await waitForSettingsIdle(page)

    // Switch to Accept Edits
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-acceptEdits"]').click()
    await expect(trigger).toContainText('Accept Edits')
    await waitForSettingsIdle(page)

    // Switch to Bypass Permissions
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-bypassPermissions"]').click()
    await expect(trigger).toContainText('Bypass Permissions')
    await waitForSettingsIdle(page)

    // Switch back to Default
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-default"]').click()
    await expect(trigger).toContainText('Default')
  })

  test('switch model', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Change model to Haiku (default is Sonnet, dropdown auto-closes on select)
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-haiku"]').click()
    await expect(trigger).toContainText('Haiku')
  })

  test('switch to model with bracket characters', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Switch to Sonnet[1m] — model name contains brackets that must be
    // properly escaped in the shell command when spawning Claude Code.
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-sonnet\\[1m\\]"]').click()
    await expect(trigger).toContainText('Sonnet (1M context)')
    await waitForSettingsIdle(page)

    // Verify agent restarted successfully by sending a message
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('What is 3+4? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')

    // Wait for an assistant response — if the agent failed to start, we
    // would see an error notification instead.
    const lastAssistant = lastAssistantBubble(page)
    await expect(lastAssistant).toContainText('7', { timeout: 30000 })
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

  test('effort hidden when haiku selected', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Default model is Sonnet — effort section should be visible; switch to Haiku
    await openSettingsMenu(page)
    await expect(page.locator('[data-testid="effort-high"]')).toBeVisible()
    await page.locator('[data-testid="model-haiku"]').click()
    await expect(trigger).toContainText('Haiku')
    await waitForSettingsIdle(page)

    // Effort section should be hidden for Haiku
    await openSettingsMenu(page)
    await expect(page.locator('[data-testid="effort-high"]')).not.toBeVisible()

    // Switch back to Sonnet — effort should reappear
    await page.locator('[data-testid="model-sonnet"]').click()
    await expect(trigger).toContainText('Sonnet')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page)
    await expect(page.locator('[data-testid="effort-high"]')).toBeVisible()
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

    // Change model to Haiku (default is Sonnet, dropdown auto-closes on select)
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-haiku"]').click()
    await expect(trigger).toContainText('Haiku')

    // Wait for restart to complete
    await page.waitForTimeout(5000)

    // Refresh the page
    await page.reload()

    // Verify Haiku is still selected after refresh
    const triggerAfter = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(triggerAfter).toBeVisible()
    await expect(triggerAfter).toContainText('Haiku')
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
    await page.keyboard.press('Meta+Enter')

    // Wait for a response (ensures init message and session ID are stored)
    const lastAssistant1 = lastAssistantBubble(page)
    await expect(lastAssistant1).toContainText('2', { timeout: 30000 })

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

    // Count existing assistant bubbles before sending the next message.
    const assistantBubblesBefore = await page.locator(ASSISTANT_BUBBLE_SELECTOR).count()

    // Send a message to trigger agent re-launch via ensureAgentActive
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')

    // Wait for a NEW assistant bubble to appear (not the old "2" response).
    await expect(page.locator(ASSISTANT_BUBBLE_SELECTOR)).toHaveCount(assistantBubblesBefore + 1, { timeout: 30000 })

    // The new bubble (last one) should contain "4".
    const lastAssistant2 = lastAssistantBubble(page)
    await expect(lastAssistant2).toContainText('4', { timeout: 30000 })

    // Verify Plan Mode is still selected after worker restart
    await expect(trigger).toContainText('Plan Mode')
  })

  test('interrupt via control request', async ({ authenticatedWorkspace, page }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a quick message to ensure the agent is fully started
    await editor.click()
    await page.keyboard.type('What is 1+1? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')
    const lastAssistant1 = lastAssistantBubble(page)
    await expect(lastAssistant1).toContainText('2', { timeout: 30000 })

    // Verify the agent is still responsive after interrupt by sending another message
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')
    const lastAssistant2 = lastAssistantBubble(page)
    await expect(lastAssistant2).toContainText('4', { timeout: 30000 })
  })

  test('model/effort items not disabled when idle', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Open dropdown while agent is idle (not streaming)
    await openSettingsMenu(page)

    // Verify all model items are enabled (not disabled) when idle
    await expect(page.locator('[data-testid="model-haiku"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="model-sonnet"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="model-sonnet\\[1m\\]"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="model-opus"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="model-opus\\[1m\\]"]')).not.toHaveAttribute('data-disabled', '')

    // Verify effort items are enabled when idle (max is only shown for opus)
    await expect(page.locator('[data-testid="effort-auto"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="effort-low"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="effort-medium"]')).not.toHaveAttribute('data-disabled', '')
    await expect(page.locator('[data-testid="effort-high"]')).not.toHaveAttribute('data-disabled', '')
    // Max effort is hidden for non-opus models (default is Sonnet)
    await expect(page.locator('[data-testid="effort-max"]')).not.toBeVisible()

    // Verify permission mode items are enabled when idle
    await expect(page.locator('[data-testid="permission-mode-default"]')).not.toHaveAttribute('data-disabled', '')

    // Verify "Disabled while running" footnote is NOT visible when idle
    await expect(page.locator('[data-testid="settings-disabled-footnote"]')).not.toBeVisible()

    await page.keyboard.press('Escape')
  })

  test('settings change notification appears in chat', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Change model to Haiku (default is Sonnet) — should produce a notification
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-haiku"]').click()
    await expect(trigger).toContainText('Haiku')

    // Verify the notification bubble appears in chat
    await expect(page.getByText('Model (Sonnet \u2192 Haiku)')).toBeVisible()
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

  test('no thinking indicator when switching settings', async ({ authenticatedWorkspace, page }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Install a MutationObserver to detect even a brief flash of the thinking indicator.
    // The ThinkingIndicator element is always in the DOM (collapsed via grid-template-rows: 0fr),
    // so we check whether it becomes visually expanded (grid-template-rows: 1fr / opacity: 1).
    await page.evaluate(() => {
      (window as any).__thinkingIndicatorSeen = false
      const observer = new MutationObserver(() => {
        const el = document.querySelector('[data-testid="thinking-indicator"]') as HTMLElement | null
        if (el && el.style.gridTemplateRows === '1fr') {
          (window as any).__thinkingIndicatorSeen = true
        }
      })
      observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['style'] })
      ;(window as any).__thinkingObserver = observer
    })

    // Switch permission mode to Plan Mode
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-plan"]').click()
    await expect(trigger).toContainText('Plan Mode')
    await waitForSettingsIdle(page)

    // Switch model to Haiku (effort section hidden for Haiku)
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-haiku"]').click()
    await expect(trigger).toContainText('Haiku')
    await waitForSettingsIdle(page)

    // Switch model back to Sonnet so effort section re-appears, then switch effort to High
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-sonnet"]').click()
    await expect(trigger).toContainText('Sonnet')
    await waitForSettingsIdle(page)

    await openSettingsMenu(page)
    await page.locator('[data-testid="effort-high"]').click()
    await waitForSettingsIdle(page)

    // Wait a moment for any delayed status events
    await page.waitForTimeout(2000)

    // Verify indicator was never shown
    const sawThinking = await page.evaluate(() => {
      (window as any).__thinkingObserver?.disconnect()
      return (window as any).__thinkingIndicatorSeen
    })
    expect(sawThinking).toBe(false)

    // Direct check too
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
  })

  test('permission mode change in new agent tab targets correct agent', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Verify first agent starts with Default mode
    const firstTriggerText = await getTriggerText(page)
    expect(firstTriggerText).toContain('Default')

    // Open a second agent tab
    await openAgentViaUI(page)

    // The new tab should also start with Default mode
    await expect(trigger).toContainText('Default')

    // Switch the new agent to Plan Mode
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-plan"]').click()
    await expect(trigger).toContainText('Plan Mode')
    await waitForSettingsIdle(page)

    // Verify the notification appears in the new agent's chat (not the first agent's)
    await expect(page.getByText('Mode (Default → Plan Mode)')).toBeVisible()

    // Switch back to the first agent tab
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await agentTabs.first().click()

    // First agent should still be in Default mode
    await expect(trigger).toContainText('Default')
    // And should NOT have the permission mode notification
    await expect(page.getByText('Mode (Default → Plan Mode)')).not.toBeVisible()
  })

  test('settings loading indicator on trigger', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Change model to Haiku (default is Sonnet) — trigger should show spinner briefly
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-haiku"]').click()

    // The trigger should show a loading spinner
    const loadingSpinner = page.locator('[data-testid="settings-loading-spinner"]')
    await expect(loadingSpinner).toBeVisible()

    // Eventually the spinner disappears after statusChange arrives
    await expect(loadingSpinner).not.toBeVisible()
    await expect(trigger).toContainText('Haiku')
  })
})
