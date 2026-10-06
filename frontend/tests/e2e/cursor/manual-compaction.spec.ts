import { cursorTest } from '../cursor-fixtures'
import { exerciseCursorCompactAsText } from './compactionScenario'

cursorTest('passes the slash command to the model in ACP mode', async ({ native }) => {
  await exerciseCursorCompactAsText(native)
})
