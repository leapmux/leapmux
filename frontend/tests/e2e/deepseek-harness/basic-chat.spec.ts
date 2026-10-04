import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { nativeContext } from './scenarios'

deepseekHarnessTest('ends the native turn and keeps its answer after reload', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseBasicChat(context)
})
