import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

codebuddyTest('closes the native provider and its owned tool process', async ({ authenticatedCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId })
  await exerciseCloseAgent(context)
})
