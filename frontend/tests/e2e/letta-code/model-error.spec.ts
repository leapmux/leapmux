import { exerciseModelError } from '../helpers/nativeModelError'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('shows the native model error and runs a later valid turn', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseModelError(context, { queueAfterFailure: 'running' })
})
