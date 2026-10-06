import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

codebuddyTest('clears native context while keeping the saved LeapMux rows', async ({ authenticatedCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId })
  await exerciseSessionReset(context)
})
