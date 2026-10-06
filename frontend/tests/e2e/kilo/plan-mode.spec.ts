import { kiloTest } from '../kilo-fixtures'
import { exercisePlanAndEffort } from '../opencode/settingsScenario'
import { KILO_PLAN_REMINDER, nativeContext } from './scenarios'

kiloTest('plan-mode: keeps its Plan mode and effort after a turn and reload', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  await exercisePlanAndEffort(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId }), 'mode', KILO_PLAN_REMINDER)
})
