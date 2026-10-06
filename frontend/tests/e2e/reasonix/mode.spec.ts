import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixPlanAndNormalWrites } from './planWriteScenario'
import { exerciseReasonixSessionSettings } from './settingsScenario'

reasonixTest('mode: applies Reasonix session settings and preserves them after reload', async ({ native }) => {
  await exerciseReasonixSessionSettings(native)
})

reasonixTest('refuses a native write in Plan mode and asks in Normal mode', async ({ native }) => {
  await exerciseReasonixPlanAndNormalWrites(native)
})
