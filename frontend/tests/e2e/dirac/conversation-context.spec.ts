import { diracTest } from '../dirac-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { nativeContext } from './scenarios'

diracTest('carries the earlier user prompt and assistant answer into the next native request', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseConversationContext(context)
})
