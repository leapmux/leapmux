import { kiloTest } from '../kilo-fixtures'
import { exerciseKiloGoal, KILO_ACP_IDLE_FALLBACK_MS } from './goalScenario'

kiloTest.setTimeout(KILO_ACP_IDLE_FALLBACK_MS * 4)

kiloTest('sets, pauses, resumes, and clears the native goal', async ({ native }) => {
  await exerciseKiloGoal(native)
})
