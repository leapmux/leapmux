import { exerciseNativePermissionWrite, exerciseNativeToolWrite } from '../helpers/nativePermission'
import { expectNativeOptionValue } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

museTest('applies native approval modes and keeps their tool behavior after reload', async ({ native }) => {
  for (const mode of ['promptUnmatched', 'allowAll']) {
    await waitForNativeSettingsHydrated(native.page)
    await chooseSettingsOption(native.page, `permissionMode-${mode}`)
    await waitForSettingsIdle(native.page)
    for (const reload of [false, true]) {
      if (reload) {
        await native.page.reload()
        await waitForNativeSettingsHydrated(native.page)
      }
      await expectSettingsOptionChosen(native.page, `permissionMode-${mode}`)
      await expectNativeOptionValue(native, 'permissionMode', mode)
      if (mode === 'promptUnmatched')
        await exerciseNativePermissionWrite(native)
      else
        await exerciseNativeToolWrite(native, { permission: 'absent' })
    }
  }
})
