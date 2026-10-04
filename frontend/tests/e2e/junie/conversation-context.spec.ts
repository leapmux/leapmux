import { exerciseConversationContext } from '../helpers/nativeConversation'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest('carries the earlier user prompt and assistant answer into the next native request', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseConversationContext(context)
})
