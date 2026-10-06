import { commandCodeTest } from '../command-code-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { nativeContext } from './scenarios'

commandCodeTest('shows the native model failure and accepts a later valid turn', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await exerciseModelError(context, { queueAfterFailure: 'running' })
})
