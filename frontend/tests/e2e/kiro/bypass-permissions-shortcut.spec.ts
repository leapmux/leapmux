import { expect } from '@playwright/test'
import { KIRO_OPTION, KIRO_POLICY_PRESET } from '../../../src/generated/contracts/kiro-protocol'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { expectPermissionShortcuts, expectSettingsOptionChosen, openWorkspace, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { KIRO_AGENT, nativeContext } from './scenarios'

kiroTest.describe('Kiro settings', () => {
  // Kiro has no preset between its own rules and every call, so Smart has no match.
  // Bypass states the allow-all preset, which Kiro reads when the session opens
  // again. A command that writes to the working directory, here a removal, then
  // runs without a permission request.
  kiroTest('bypass runs a write without asking', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, `${KIRO_OPTION.PolicyPreset}-ask`)
    await expectPermissionShortcuts(page, { smart: 'absent', bypass: 'offered' })

    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseBypassPermissions(context, {
      settingsProof: agent => expect(nativeOptionValue(agent, KIRO_OPTION.PolicyPreset)).toBe(KIRO_POLICY_PRESET.AllowAll),
    })
    await expectSettingsOptionChosen(page, `${KIRO_OPTION.PolicyPreset}-${KIRO_POLICY_PRESET.AllowAll}`)
  })
})
