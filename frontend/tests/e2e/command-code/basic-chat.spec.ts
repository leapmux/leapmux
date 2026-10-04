import { commandCodeTest } from '../command-code-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { nativeContext } from './scenarios'

commandCodeTest('ends the native turn and keeps its answer after reload', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  await exerciseBasicChat(context)
})
