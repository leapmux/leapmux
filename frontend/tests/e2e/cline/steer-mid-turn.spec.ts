import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest.describe('Cline steering', () => {
  clineTest('steers a running turn after its tool', async ({ authenticatedClineWorkspace, page, modelScript, leapmuxServer }) => {
    await exerciseSteerAfterTool({ page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE })
  })
})
