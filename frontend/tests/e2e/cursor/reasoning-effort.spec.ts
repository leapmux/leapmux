import { cursorTest } from '../cursor-fixtures'
import { exerciseCursorReasoningEffort } from './settingsScenario'

cursorTest('applies the selected native model effort before and after reload', async ({ native }) => {
  await exerciseCursorReasoningEffort(native)
})
