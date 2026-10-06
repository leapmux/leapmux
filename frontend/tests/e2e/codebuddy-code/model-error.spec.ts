import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { nativeContext } from './scenarios'

codebuddyTest('shows the native model error and runs a later valid turn', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await exerciseModelError(context, { queueAfterFailure: 'running' })
})
