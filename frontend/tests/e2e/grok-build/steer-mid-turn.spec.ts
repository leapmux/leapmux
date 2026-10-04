import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from '../grok-fixtures'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { openWorkspace } from '../helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest.describe('Grok Build settings, folder trust and MCP forms', () => {
  grokTest('steers a queued message into the active turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { approvalMode: 'always-approve' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseProviderSteer(page, modelScript, AgentProvider.GROK_BUILD)
  })
})
