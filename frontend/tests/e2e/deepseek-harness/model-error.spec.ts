import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { nativeContext } from './scenarios'

deepseekHarnessTest('shows the native model failure and accepts a later valid turn', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseModelError(context, { queueAfterFailure: 'running' })
})
