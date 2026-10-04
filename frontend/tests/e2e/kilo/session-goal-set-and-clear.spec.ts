import { KILO_E2E_SKIP_REASON, kiloTest } from '../kilo-fixtures'
import { exerciseKiloGoal, KILO_ACP_IDLE_FALLBACK_MS, scriptedObjective } from './goalScenario'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')
kiloTest.setTimeout(KILO_ACP_IDLE_FALLBACK_MS * 4)

kiloTest('sets, pauses, resumes, and clears the native goal', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  void authenticatedKiloWorkspace
  await modelScript.queue(
    { text: 'The first Kilo goal turn finished.' },
    { text: 'The resumed Kilo goal turn is held.', gate: 'kilo-resumed-goal' },
  )
  const objective = scriptedObjective(modelScript, 'Keep the Kilo session goal until the browser clears it.')
  await exerciseKiloGoal(page, modelScript, objective, leapmuxServer)
})
