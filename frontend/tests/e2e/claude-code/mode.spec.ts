import { expect } from '@playwright/test'
import { CLAUDE_MODE } from '../../../src/generated/contracts/claude-protocol'
import { claudeTest, claudeProcessTest as test } from '../claude-fixtures'
import { systemText } from '../helpers/mockModelScript'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { currentNativeAgent, nativeOptionGroup } from '../helpers/nativeScenario'
import { exerciseNativeOption, exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { thinkingIndicatorShownDuring } from '../helpers/thinkingIndicatorWatch'
import { chooseSettingsOption, composerEditor, expectSettingsChip, permissionModeOffered, settingsBar, visibleOnly, waitForAgentIdle, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

test.describe('Agent Settings', () => {
  test('switch permission modes', async ({ authenticatedWorkspace, page, native }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()
    await waitForNativeSettingsHydrated(page)
    const nativeModes = nativeOptionGroup(await currentNativeAgent(native), 'permissionMode')?.options.map(option => option.id) ?? []
    const supportedModes = Object.values(CLAUDE_MODE).filter(mode => mode !== CLAUDE_MODE.Auto || nativeModes.includes(mode))
    expect([...nativeModes].sort()).toEqual([...supportedModes].sort())
    await expectSettingsOptionsOffered(page, 'permissionMode', supportedModes)

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
    await expect(visibleOnly(page.getByText('Permission Mode (Default \u2192 Plan Mode)', { exact: true }))).toHaveCount(1)
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
      expect(systemText(request.protocol, request.body)).toMatch(/plan mode is active/i)
    },
  })
})

claudeTest('returns from Plan to Default with one notice and native proof after each reload', async ({ native }) => {
  const { page } = native
  await waitForNativeSettingsHydrated(page)
  const initialMode = nativeOptionGroup(await currentNativeAgent(native), 'permissionMode')?.currentValue
  if (initialMode !== CLAUDE_MODE.Auto && initialMode !== CLAUDE_MODE.Default)
    throw new Error('The native Claude mode proof requires Auto Mode or Default at startup.')
  const initialLabel = initialMode === CLAUDE_MODE.Auto ? 'Auto Mode' : 'Default'
  await expectSettingsChip(page, initialLabel)
  // Consecutive changes to one option keep the first old value in their notice.
  // Start from the native startup mode so the Plan change has no earlier setup change to merge.
  for (const change of [
    { value: CLAUDE_MODE.Plan, label: 'Plan Mode', previousLabel: initialLabel },
    { value: CLAUDE_MODE.Default, label: 'Default', previousLabel: 'Plan Mode' },
  ]) {
    const notice = await chooseNativeModeWithNotice(native, { ...change, groupId: 'permissionMode', noticeLabel: 'Permission Mode' })
    const nativeProof = (request: Awaited<ReturnType<typeof sendNativeAnswer>>): void => {
      expect(request.protocol).toBe('anthropic-messages')
      const instructions = systemText(request.protocol, request.body)
      expect(instructions).not.toBe('')
      // Claude retains earlier mode reminders. The latest native reminder states the current mode.
      const modeReminders = [...instructions.matchAll(/^(?:Plan mode is active\.|## Exited Plan Mode\n\nYou have exited plan mode\. You can now make edits, run tools, and take actions\.)/gm)]
      expect(modeReminders.at(-1)?.[0]).toBe(change.value === CLAUDE_MODE.Plan
        ? 'Plan mode is active.'
        : '## Exited Plan Mode\n\nYou have exited plan mode. You can now make edits, run tools, and take actions.')
      if (change.value === CLAUDE_MODE.Plan)
        expect(instructions).toMatch(/plan mode is active/i)
    }
    nativeProof(await sendNativeAnswer(native, 'Reply once under the selected setting.', `The native ${change.label} turn answered.`))
    await exerciseRestoredNativeOption(native, { groupId: 'permissionMode', value: change.value, nativeProof })
    await expectSettingsChip(page, change.label)
    await notice.assertStored()
  }
})
