import { LETTA_MODE } from '../../../src/generated/contracts/letta-protocol'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { expectPermissionShortcuts, expectSettingsChip, expectSettingsOptionChosen, waitForNativeSettingsHydrated } from '../helpers/ui'
import { expect, lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code bypass permissions', () => {
  // The workspace opens in Standard, which asks before a tool runs. The Bypass shortcut selects Unrestricted, which
  // answers every tool call at once. A real removal must then run with no banner, before and after a reload.
  lettaTest('the Bypass shortcut switches the session to Unrestricted', async ({ askingLettaWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingLettaWorkspace.workspaceId })
    await waitForNativeSettingsHydrated(page)
    await expectPermissionShortcuts(page, { bypass: 'offered' })

    await exerciseBypassPermissions(context, {
      settingsProof: agent => expect(nativeOptionValue(agent, 'permissionMode')).toBe(LETTA_MODE.Unrestricted),
    })
    await expectSettingsChip(page, 'Unrestricted')
    // The mode group states the same value that the shortcut set.
    await expectSettingsOptionChosen(page, `permissionMode-${LETTA_MODE.Unrestricted}`)
  })
})
