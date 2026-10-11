import { opencodeTest } from '../opencode-fixtures'
import { exercisePlanAndEffort } from './settingsScenario'

opencodeTest('plan-mode: keeps its Plan mode and effort after a turn and reload', async ({ native }) => {
  await exercisePlanAndEffort(native, 'mode')
})
