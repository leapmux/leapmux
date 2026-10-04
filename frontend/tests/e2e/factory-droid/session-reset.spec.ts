import { droidTest } from '../droid-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

droidTest('clears native context while keeping the saved LeapMux rows', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseSessionReset(context)
})
