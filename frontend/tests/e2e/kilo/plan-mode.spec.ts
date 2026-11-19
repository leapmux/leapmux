import { kiloTest } from '../kilo-fixtures'
import { KILO_PLAN_REMINDER } from './scenarios'
import { exercisePlanAndEffort } from './settingsScenario'

kiloTest('plan-mode: keeps its Plan mode and effort after a turn and reload', async ({ native }) => {
  await exercisePlanAndEffort(native, 'mode', KILO_PLAN_REMINDER)
})
