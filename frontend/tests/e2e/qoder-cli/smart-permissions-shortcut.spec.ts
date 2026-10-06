import { QODER_MODE } from '../../../src/generated/contracts/qoder-protocol'
import { applyPermissionPreset, expectPermissionShortcuts, expectSettingsOptionChosen, waitForSettingsHydrated } from '../helpers/ui'
import { expectQoderModeChip, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI settings', () => {
  // Smart selects Qoder's Auto mode, whose own classifier judges each risky call through a model request. The suite
  // scripts no answer for that request, so this test proves the shortcut and the mode, not a native tool run.
  qoderTest('the Smart shortcut selects Auto mode', async ({ authenticatedQoderWorkspace, page }) => {
    void authenticatedQoderWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectPermissionShortcuts(page, { smart: 'offered' })

    await applyPermissionPreset(page, 'smart')
    await expectQoderModeChip(page, 'Auto')
    await expectSettingsOptionChosen(page, `permissionMode-${QODER_MODE.Auto}`)
  })
})
