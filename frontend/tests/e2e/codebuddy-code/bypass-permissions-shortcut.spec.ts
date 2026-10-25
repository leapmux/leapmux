import { expect } from '@playwright/test'
import { CODEBUDDY_MODE } from '../../../src/generated/contracts/codebuddy-protocol'
import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectPermissionShortcuts, expectSettingsChip, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest('offers both shortcuts and runs a native tool after Bypass', async ({ native }) => {
    const { page } = native
    await exerciseBypassPermissions(native, {
      // The workspace opens in Bypass. Default asks before a tool, so the shortcut must change the native behavior.
      prepare: async () => {
        await waitForSettingsHydrated(page, 'permissionMode')
        await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.Default}`)
        await waitForSettingsIdle(page)
        await expectSettingsOptionChosen(page, `permissionMode-${CODEBUDDY_MODE.Default}`)
        await expectPermissionShortcuts(page, { smart: 'offered', bypass: 'offered' })
      },
      settingsProof: agent => expect(nativeOptionValue(agent, 'permissionMode')).toBe(CODEBUDDY_MODE.BypassPermissions),
    })
    await expectSettingsChip(page, 'Bypass Permissions')
  })
})

codebuddyTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ askingCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingCodebuddyWorkspace.workspaceId })
  await exerciseMidTurnBypass(native)
})
