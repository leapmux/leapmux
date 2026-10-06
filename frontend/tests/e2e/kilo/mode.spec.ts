import { kiloTest } from '../kilo-fixtures'
import { exercisePlanAndEffort } from '../opencode/settingsScenario'
import { KILO_PLAN_REMINDER } from './scenarios'

kiloTest('mode: keeps its Plan mode and effort after a turn and reload', async ({ native }) => {
  await exercisePlanAndEffort(native, 'mode', KILO_PLAN_REMINDER)
})
