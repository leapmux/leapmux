import { expect } from '@playwright/test'
import { CLINE_PERMISSION_MODE } from '../../../src/generated/contracts/cline-protocol'
import { clineTest } from '../cline-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { expectPermissionShortcuts, expectSettingsChip } from '../helpers/ui'
import { nativeContext } from './scenarios'

/**
 * The Bypass shortcut selects the provider's native permission preset. A real tool must execute without a permission banner.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.describe('Cline control requests', () => {
  clineTest('runs every call without a banner in Auto-approve, which the Bypass shortcut selects', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    await expectSettingsChip(page, 'Act')
    // Cline has no mode that asks for the risky calls alone, so it offers no Smart
    // shortcut. Bypass selects Auto-approve, which applies to the next call at once.
    await expectPermissionShortcuts(page, { smart: 'absent', bypass: 'offered' })
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    await exerciseBypassPermissions(context, {
      settingsProof: agent => expect(nativeOptionValue(agent, 'permissionMode')).toBe(CLINE_PERMISSION_MODE.AutoApprove),
    })
    await expectSettingsChip(page, 'Auto-approve')
  })
})
