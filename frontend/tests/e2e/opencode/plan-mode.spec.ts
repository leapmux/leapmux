import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { opencodeTest } from '../opencode-fixtures'
import { exercisePlanAndEffort } from './settingsScenario'

opencodeTest('plan-mode: keeps its Plan mode and effort after a turn and reload', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  await exercisePlanAndEffort({ page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }, 'mode')
})
