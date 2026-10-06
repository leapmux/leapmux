import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_AGENT, grokTest } from '../grok-fixtures'
import { exerciseProviderSteer } from '../helpers/providerSteer'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'

grokTest.describe('Grok Build settings, folder trust and MCP forms', () => {
  grokTest('steers a queued message into the active turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await exerciseProviderSteer(page, modelScript, AgentProvider.GROK_BUILD)
  })
})
