import { cursorTest } from '../cursor-fixtures'
import { exerciseCursorSelectedPlanSettings } from './settingsScenario'

cursorTest('keeps a selected model and Plan mode after a turn and reload', async ({ native }) => {
  await exerciseCursorSelectedPlanSettings(native, 'model')
})
