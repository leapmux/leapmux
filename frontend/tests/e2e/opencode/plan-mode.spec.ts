import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from '../opencode-fixtures'
import { exercisePlanAndEffort } from './settingsScenario'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest('plan-mode: keeps its Plan mode and effort after a turn and reload', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  await exercisePlanAndEffort({ page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }, 'mode')
})
