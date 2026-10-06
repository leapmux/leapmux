import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { nativeContext } from './scenarios'

deepseekHarnessTest('uses prior user and assistant context in the next native request', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseConversationContext(context)
})
