import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { kiloTest } from '../kilo-fixtures'
import { exercisePlanAndEffort } from '../opencode/settingsScenario'
import { KILO_PLAN_REMINDER } from './scenarios'

kiloTest('plan-mode: keeps its Plan mode and effort after a turn and reload', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  await exercisePlanAndEffort({ page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }, 'mode', KILO_PLAN_REMINDER)
})
