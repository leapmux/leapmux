import { CODEBUDDY_MODE } from '../../../src/generated/contracts/codebuddy-protocol'
import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectPermissionShortcuts, expectSettingsChip, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest('offers Bypass but no Smart shortcut and runs a native tool after Bypass', async ({ native }) => {
    const { page } = native
    await exerciseBypassPermissions(native, {
      // The workspace opens in Bypass. Default asks before a tool, so the shortcut must change the native behavior.
      prepare: async () => {
        await waitForSettingsHydrated(page, 'permissionMode')
        await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.Default}`)
        await waitForSettingsIdle(page)
        await expectSettingsOptionChosen(page, `permissionMode-${CODEBUDDY_MODE.Default}`)
        await expectPermissionShortcuts(page, { smart: 'absent', bypass: 'offered' })
      },
      settingsProof: agent => expect(nativeOptionValue(agent, 'permissionMode')).toBe(CODEBUDDY_MODE.BypassPermissions),
    })
    await expectSettingsChip(page, 'Bypass Permissions')
  })
})
