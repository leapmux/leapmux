import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { KILO_E2E_SKIP_REASON, kiloTest } from '../kilo-fixtures'
import { exercisePlanAndEffort } from './settingsScenario'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

kiloTest('mode: keeps its Plan mode and effort after a turn and reload', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  await exercisePlanAndEffort({ page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }, 'mode')
})
