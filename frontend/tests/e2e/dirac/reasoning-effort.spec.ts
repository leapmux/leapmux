import { expect } from '@playwright/test'
import { diracTest } from '../dirac-fixtures'
import { DIRAC_ALT_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

diracTest.describe('Dirac settings apply', () => {
  diracTest('switches the mode and the effort, and keeps them after reload', async ({ authenticatedDiracWorkspace, page }) => {
    void authenticatedDiracWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Act')

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await chooseSettingsOption(page, 'reasoning_effort-high')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'High')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'High')

    await chooseSettingsOption(page, 'permissionMode-act')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Act')
  })

  diracTest('sends the selected effort in the next native request', async ({ native, page }) => {
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Act')
    await exerciseNativeOption(native, {
      groupId: 'reasoning_effort',
      value: 'low',
      nativeProof: request => expect(request.body).toMatchObject({ reasoning_effort: 'low' }),
    })
    await expectSettingsChip(page, 'Low')
  })
})

// Dirac's effort does not depend on the model, and the ACP base writes the stored effort after the model
// (`EffortConfigID`, `dirac/start.go`). Dirac starts at Medium, so Low differs from a reset.
diracTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'reasoning_effort', value: 'low' },
    model: DIRAC_ALT_MODEL_ID,
    nativeProof: request => expect(request.body).toMatchObject({ model: DIRAC_ALT_MODEL_ID, reasoning_effort: 'low' }),
  })
})
