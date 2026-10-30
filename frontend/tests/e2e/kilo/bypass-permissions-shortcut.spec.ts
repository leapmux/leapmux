import { expect } from '@playwright/test'
import { CLIENT_APPROVAL_OPTION } from '../../../src/generated/contracts/client-approval'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { expectPermissionShortcuts, waitForNativeSettingsHydrated } from '../helpers/ui'
import { kiloTest } from '../kilo-fixtures'

// Kilo's default shell rule asks, so each command asks unless Bypass answers the request.
kiloTest('offers Bypass and runs a native command without a prompt before and after reload', async ({ native }) => {
  await waitForNativeSettingsHydrated(native.page)
  await expectPermissionShortcuts(native.page, { smart: 'absent', bypass: 'offered' })
  await exerciseBypassPermissions(native, {
    settingsProof: agent => expect(nativeOptionValue(agent, CLIENT_APPROVAL_OPTION.AutoApprove)).toBe('true'),
  })
})

kiloTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ native }) => {
  await exerciseMidTurnBypass(native)
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/515.
