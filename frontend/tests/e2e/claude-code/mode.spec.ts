import { expect } from '@playwright/test'
import { claudeTest, claudeProcessTest as test } from '../claude-fixtures'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { thinkingIndicatorShownDuring } from '../helpers/thinkingIndicatorWatch'
import { chooseSettingsOption, composerEditor, expectSettingsChip, permissionModeOffered, settingsBar, visibleOnly, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

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
    const autoOffered = await permissionModeOffered(page, 'auto')
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
    const editor = composerEditor(page)
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
    const editor = composerEditor(page)
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
    await expect(page.locator('[data-testid="thinking-indicator"]:visible')).toHaveCount(0)
  })
})

claudeTest('applies native Plan instructions before and after restoring the selected mode', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'permissionMode',
    value: 'plan',
    nativeProof: (request) => {
      expect(request.protocol).toBe('anthropic-messages')
      expect(nativeModelInstructionText(request)).toMatch(/plan mode is active/i)
    },
  })
})
