import { commandCodeTest } from '../command-code-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { nativeContext } from './scenarios'

commandCodeTest('uses the earlier prompt and answer in the next native request', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  await exerciseConversationContext(context)
})
