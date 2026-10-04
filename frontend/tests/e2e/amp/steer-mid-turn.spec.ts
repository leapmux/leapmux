import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'

ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest('steers a running turn after its tool', async ({ authenticatedAmpWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseSteerAfterTool({ page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP })
})
