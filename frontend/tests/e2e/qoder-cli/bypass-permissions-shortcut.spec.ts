import { expect } from '@playwright/test'
import { QODER_MODE } from '../../../src/generated/contracts/qoder-protocol'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectPermissionShortcuts, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'
import { expectQoderModeChip, nativeContext } from './scenarios'

qoderTest('offers Bypass and runs a native command without a prompt before and after reload', async ({ native }) => {
  const { page } = native
  await exerciseBypassPermissions(native, {
    // The workspace opens in Accept Edits. Default asks before the command, so the shortcut must change the native behavior.
    prepare: async () => {
      await waitForSettingsHydrated(page, 'permissionMode')
      await chooseSettingsOption(page, `permissionMode-${QODER_MODE.Default}`)
      await waitForSettingsIdle(page)
      await expectSettingsOptionChosen(page, `permissionMode-${QODER_MODE.Default}`)
      await expectPermissionShortcuts(page, { smart: 'offered', bypass: 'offered' })
    },
    settingsProof: agent => expect(nativeOptionValue(agent, 'permissionMode')).toBe(QODER_MODE.BypassPermissions),
  })
  await expectQoderModeChip(page, 'Bypass Permissions')
})

// Qoder withdraws the pending ask itself (control_cancel_request) when the mode changes to Bypass, and runs the tool.
qoderTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ askingQoderWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({
    page,
    modelScript,
    leapmuxServer,
    workspaceId: askingQoderWorkspace.workspaceId,
  })
  await exerciseMidTurnBypass(native)
})
