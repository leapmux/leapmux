import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { nativeContext } from './scenarios'

deepseekHarnessTest('ends the native turn and keeps its answer after reload', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseBasicChat(context)
})
