import { expect } from '@playwright/test'
import { CLAUDE_MODE } from '../../../src/generated/contracts/claude-protocol'
import { claudeTest as test } from '../claude-fixtures'
import { exerciseSmartPermissions } from '../helpers/nativeBypassPermissions'
import { currentNativeAgent, expectNativeOptionValue, nativeOptionGroup, nativeOptionValue } from '../helpers/nativeScenario'
import { applyPermissionPreset, chooseSettingsOption, expectPermissionShortcuts, expectSettingsChip, permissionModeOffered, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

test.describe('Agent Settings', () => {
  test('permission shortcuts use the Claude modes that the session offers', async ({ authenticatedClaudeWorkspace, page }) => {
    void authenticatedClaudeWorkspace
    await waitForSettingsHydrated(page)
    // Smart selects Auto Mode. The native startup result determines whether the session offers that mode.
    // Check either catalog outcome. The separate native case requires Auto support and its actual command result.
    // A new Claude session asks for Auto Mode, and it offers Auto only when the CLI accepted that request. So a session
    // that offers Auto starts in it, and the menu disables the shortcut of the preset that is already active.
    const autoOffered = await permissionModeOffered(page, CLAUDE_MODE.Auto)
    if (autoOffered)
      await expectSettingsChip(page, 'Auto Mode')
    await expectPermissionShortcuts(page, { smart: autoOffered ? 'disabled' : 'absent', bypass: 'offered' })

    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Bypass Permissions')
    await expectPermissionShortcuts(page, { smart: autoOffered ? 'offered' : 'absent', bypass: 'disabled' })

    if (autoOffered) {
      await applyPermissionPreset(page, 'smart')
      await expectSettingsChip(page, 'Auto Mode')
      await expectPermissionShortcuts(page, { smart: 'disabled', bypass: 'offered' })
    }
  })
})

test('executes the native Smart command with confirmed Auto mode before and after reload', async ({ native }) => {
  const { page } = native
  const before = await currentNativeAgent(native)
  expect(nativeOptionGroup(before, 'permissionMode')?.options.map(option => option.id)).toContain(CLAUDE_MODE.Auto)
  await exerciseSmartPermissions(native, {
    prepare: async () => {
      await chooseSettingsOption(page, `permissionMode-${CLAUDE_MODE.Default}`)
      await waitForSettingsIdle(page)
      await expectNativeOptionValue(native, 'permissionMode', CLAUDE_MODE.Default)
    },
    settingsProof: (agent) => {
      expect(nativeOptionValue(agent, 'permissionMode')).toBe(CLAUDE_MODE.Auto)
    },
  })
})
