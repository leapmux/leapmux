import { REASONIX_MODE } from '../../../src/generated/contracts/reasonix-protocol'
import { expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { waitForNativeSettingsHydrated } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixPlanAndNormalWrites } from './planWriteScenario'
import { exerciseReasonixSessionSettings } from './settingsScenario'

reasonixTest('mode: applies Reasonix session settings and preserves them after reload', async ({ native }) => {
  await waitForNativeSettingsHydrated(native.page)
  await expectSettingsOptionsOffered(native.page, 'permissionMode', Object.values(REASONIX_MODE))
  await exerciseReasonixSessionSettings(native)
})

reasonixTest('refuses a native write in Plan mode and asks in Normal mode', async ({ native }) => {
  await exerciseReasonixPlanAndNormalWrites(native)
})
