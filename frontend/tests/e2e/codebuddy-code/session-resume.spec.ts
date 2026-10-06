import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

codebuddyTest('reopens a completed picker session and restores its saved Worker rows', async ({ authenticatedCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId })
  await exerciseSessionResume(context)
})
