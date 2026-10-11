import { cursorTest } from '../cursor-fixtures'
import { exerciseCursorChildIdentity, exerciseCursorTaskDelegation } from './childScenario'

cursorTest('Task delegation creates a registry row with a sanitized key', async ({ native }) => {
  await exerciseCursorTaskDelegation(native)
})

cursorTest('shows the actual native child identity and final reply in its tab', async ({ native }) => {
  await exerciseCursorChildIdentity(native)
})
