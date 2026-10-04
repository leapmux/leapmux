import { exerciseModelError } from '../helpers/nativeModelError'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest('shows the native model error and runs a later valid turn', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseModelError(context)
})
