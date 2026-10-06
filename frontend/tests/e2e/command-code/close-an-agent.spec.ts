import { commandCodeTest } from '../command-code-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

commandCodeTest('stops the actual provider and its owned command processes', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await exerciseCloseAgent(context)
})
