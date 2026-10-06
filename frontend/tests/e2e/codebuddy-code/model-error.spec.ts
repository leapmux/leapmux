import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { nativeContext } from './scenarios'

codebuddyTest('shows the native model error and runs a later valid turn', async ({ authenticatedCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodebuddyWorkspace.workspaceId })
  await exerciseModelError(context, { queueAfterFailure: 'running' })
})
