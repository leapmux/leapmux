import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { junieTest } from '../junie-fixtures'
import { nativeContext } from './scenarios'

junieTest('reopens a completed picker session and restores its saved Worker rows', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseSessionResume(context)
})
