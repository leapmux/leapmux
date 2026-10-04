import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { openCursorRunningChild } from './childScenario'

cursorTest('refuses native child send while the actual child task runs', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  await expectUnsupportedSubagent(context, { operation: 'send', openChild: () => openCursorRunningChild(context) })
})
