import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest('clears native context while keeping the saved LeapMux rows', async ({ authenticatedQoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedQoderWorkspace.workspaceId })
  await exerciseSessionReset(context)
})
