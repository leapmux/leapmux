import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

copilotTest('carries earlier user and assistant text into the next native context', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  await exerciseConversationContext(context)
})
