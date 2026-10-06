import { cursorTest } from '../cursor-fixtures'
import { exerciseCursorNativePlanRPC, exerciseCursorSelectedPlanSettings } from './settingsScenario'

cursorTest('mode: keeps a selected model and Plan mode after a turn and reload', async ({ native }) => {
  await exerciseCursorSelectedPlanSettings(native, 'permissionMode')
})

cursorTest('confirms Plan mode through the native set-mode RPC', async ({ native }) => {
  await exerciseCursorNativePlanRPC(native)
})
