import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { nativeContext } from './scenarios'

deepseekHarnessTest('uses prior user and assistant context in the next native request', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseConversationContext(context)
})
