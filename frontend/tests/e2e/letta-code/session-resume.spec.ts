import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('reopens a completed picker session and restores its saved Worker rows', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseSessionResume(context)
})
