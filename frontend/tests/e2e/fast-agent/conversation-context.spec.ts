import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { nativeContext } from './scenarios'

fastAgentTest('carries the earlier user prompt and assistant answer into the next native request', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseConversationContext(context)
})
