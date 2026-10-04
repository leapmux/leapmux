import { diracTest } from '../dirac-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

diracTest('reopens a completed picker session and restores its saved Worker rows', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseSessionResume(context)
})
