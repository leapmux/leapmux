import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest('clears native context while keeping the saved LeapMux rows', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseSessionReset(context)
})
