import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('clears native context while keeping the saved LeapMux rows', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseSessionReset(context)
})
