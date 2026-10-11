import { expect } from '@playwright/test'
import { MIMO_OPTION, MIMO_PERMISSION_POLICY } from '../../../src/generated/contracts/mimo-protocol'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { expectPermissionShortcuts, expectSettingsOptionChosen, openWorkspace, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { mimoTest } from '../mimo-fixtures'
import { MIMO_AGENT, nativeContext } from './scenarios'

mimoTest.describe('MiMo Code settings', () => {
  // MiMo controls native approvals through two runtime switches.
  // Bypass enables skip-all and auto-approve-delete. The second switch permits deletion without the native permission question.
  // The shared proof removes a directory, so it needs the deletion switch as well as skip-all.
  mimoTest('bypass runs a deletion without asking', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, MIMO_AGENT, { directoryPrefix: 'mimo-bypass-' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectPermissionShortcuts(page, { smart: 'absent', bypass: 'offered' })

    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseBypassPermissions(context, {
      settingsProof: agent => expect(nativeOptionValue(agent, MIMO_OPTION.PermissionPolicy)).toBe(MIMO_PERMISSION_POLICY.Bypass),
    })
    // The policy is not a status-bar axis, so its own group in the menu states it.
    await expectSettingsOptionChosen(page, `${MIMO_OPTION.PermissionPolicy}-${MIMO_PERMISSION_POLICY.Bypass}`)
  })
})
