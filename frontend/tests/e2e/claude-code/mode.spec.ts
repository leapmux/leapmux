import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest, claudeProcessTest as test } from '../claude-fixtures'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { thinkingIndicatorShownDuring } from '../helpers/thinkingIndicatorWatch'
import { chooseSettingsOption, expectSettingsChip, openAgentViaUI, openSettingsMenu, permissionModeOffered, settingsBar, visibleOnly, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

test.describe('Agent Settings', () => {
  test('switch permission modes', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Select Plan Mode. The menu closes after selection.
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')
    await waitForSettingsIdle(page)

    // Switch to Accept Edits
    await chooseSettingsOption(page, 'permissionMode-acceptEdits')
    await expectSettingsChip(page, 'Accept Edits')
    await waitForSettingsIdle(page)

    // Switch to Bypass Permissions
    await chooseSettingsOption(page, 'permissionMode-bypassPermissions')
    await expectSettingsChip(page, 'Bypass Permissions')
    await waitForSettingsIdle(page)

    // Select Don't Ask. The session always offers this mode.
    await chooseSettingsOption(page, 'permissionMode-dontAsk')
    await expectSettingsChip(page, 'Don\'t Ask')
    await waitForSettingsIdle(page)

    // Select Auto Mode when the native startup result offers it.
    // The model and administrator settings can remove this option from the catalog.
    await openSettingsMenu(page, 'permissionMode')
    const autoOffered = await page.locator('[data-testid="permissionMode-auto"]').isVisible()
    await page.keyboard.press('Escape')
    if (autoOffered) {
      await chooseSettingsOption(page, 'permissionMode-auto')
      await expectSettingsChip(page, 'Auto Mode')
      await waitForSettingsIdle(page)
    }

    // Switch back to Default
    await chooseSettingsOption(page, 'permissionMode-default')
    await expectSettingsChip(page, 'Default')
  })

  test('permission mode persistence across refresh', async ({ authenticatedWorkspace, page }) => {
    // Wait for the editor to be ready (agent is started)
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Select Plan Mode. The menu closes after selection.
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')

    // Wait for the Worker to confirm the settings before the reload.
    await waitForSettingsIdle(page)

    // Refresh the page
    await page.reload()

    // Verify Plan Mode is still selected after refresh
    const triggerAfter = settingsBar(page)
    await expect(triggerAfter).toBeVisible()
    await expectSettingsChip(page, 'Plan Mode')
  })

  test('focus returns to editor after mode change', async ({ authenticatedWorkspace, page }) => {
    // Require the editor after agent startup.
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Open dropdown and click a mode
    await chooseSettingsOption(page, 'permissionMode-plan')

    // Close the dropdown by pressing Escape
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-testid="composer-plus-popover"]')).not.toBeVisible()

    // Click the editor and verify it can receive focus
    await editor.click()
    await expect(editor).toBeFocused()
  })

  test('permission mode change notification appears in chat', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Switch to Plan Mode
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')

    // Verify the notification bubble appears in chat
    await expect(visibleOnly(page.getByText('Mode (Default \u2192 Plan Mode)'))).toBeVisible()
  })

  test('no thinking indicator when switching settings', async ({ authenticatedWorkspace, page, modelScript }) => {
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // The watch records even a short flash of the thinking indicator.
    const sawThinking = await thinkingIndicatorShownDuring(page, async () => {
      // Switch permission mode to Plan Mode
      await chooseSettingsOption(page, 'permissionMode-plan')
      await expectSettingsChip(page, 'Plan Mode')
      await waitForSettingsIdle(page)

      // Switch model to Haiku (effort section hidden for Haiku)
      await chooseSettingsOption(page, 'model-haiku')
      await expectSettingsChip(page, 'Haiku')
      await waitForSettingsIdle(page)

      // Select Sonnet to restore the effort section. Select High effort afterward.
      await chooseSettingsOption(page, 'model-sonnet')
      await expectSettingsChip(page, 'Sonnet')
      await waitForSettingsIdle(page)

      await chooseSettingsOption(page, 'effort-high')
      await waitForSettingsIdle(page)

      await waitForAgentIdle(page)
    })
    expect((await modelScript.status()).requests).toHaveLength(0)

    // Verify indicator was never shown
    expect(sawThinking).toBe(false)

    // Direct check too
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
  })

  test('permission mode change in new agent tab targets correct agent', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Verify first agent starts with Default mode
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')

    // Open a second agent tab
    await openAgentViaUI(page)

    // A new session requests Auto Mode. The CLI startup probe determines whether that mode is available.
    // Read the picker to determine the exact expected mode. An alternative regular expression can accept an incorrect default.
    await waitForSettingsHydrated(page)
    const expectedMode = await permissionModeOffered(page, 'auto') ? 'Auto Mode' : 'Default'
    await expectSettingsChip(page, expectedMode)

    // Switch the new agent to Plan Mode
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')
    await waitForSettingsIdle(page)

    // Require the notification in the new agent's chat.
    await expect(visibleOnly(page.getByText(`Mode (${expectedMode} → Plan Mode)`))).toBeVisible()

    // Switch back to the first agent tab
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await agentTabs.first().click()

    // First agent should still be in Default mode
    await expectSettingsChip(page, 'Default')
    // And should NOT have the permission mode notification
    await expect(visibleOnly(page.getByText('Mode (Default → Plan Mode)'))).not.toBeVisible()
  })

  test('settings loading indicator in the status bar', async ({ authenticatedWorkspace, page }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Select Haiku from the default Sonnet model. The chip changes before the Worker confirms the setting.
    // Require the spinner during that unconfirmed state.
    await chooseSettingsOption(page, 'model-haiku')

    const loadingSpinner = page.locator('[data-testid="settings-loading-spinner"]')
    await expect(loadingSpinner).toBeVisible()

    // Require spinner removal after statusChange arrives.
    await expect(loadingSpinner).not.toBeVisible()
    await expectSettingsChip(page, 'Haiku')
  })
})

claudeTest('applies native Plan instructions before and after restoring the selected mode', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  await exerciseNativeOption({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }, {
    groupId: 'permissionMode',
    value: 'plan',
    nativeProof: (request) => {
      expect(request.protocol).toBe('anthropic-messages')
      expect(nativeModelInstructionText(request)).toMatch(/plan mode is active/i)
    },
  })
})
