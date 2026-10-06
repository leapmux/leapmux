import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest('reopens a completed picker session and restores its saved Worker rows', async ({ authenticatedQoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedQoderWorkspace.workspaceId })
  await exerciseSessionResume(context)
})
