import { piTest } from '../pi-fixtures'
import { exercisePiForegroundChild } from './childScenario'

piTest('foreground subagent shows a live activity row', async ({ native }) => {
  await exercisePiForegroundChild(native)
})
