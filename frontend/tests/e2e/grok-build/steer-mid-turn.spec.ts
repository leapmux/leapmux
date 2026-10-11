import { grokTest } from '../grok-fixtures'
import { exerciseSteerBeforeTool } from '../helpers/nativeToolSteering'
import { openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { GROK_AGENT, nativeContext } from './scenarios'

grokTest.describe('Grok Build settings, folder trust and MCP forms', () => {
  grokTest('steers a queued message into the active turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { approvalMode: 'always-approve' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseSteerBeforeTool(context)
  })
})
