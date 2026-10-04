import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest('closes the native provider and its owned tool process', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseCloseAgent(context)
})
