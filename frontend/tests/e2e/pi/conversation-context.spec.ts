import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { piTest } from '../pi-fixtures'

piTest('carries earlier user and assistant text into the next native context', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseConversationContext(context)
})
