import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'
import { nativeContext } from './scenarios'

deepseekHarnessTest('shows the native model failure and accepts a later valid turn', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseModelError(context, { queueAfterFailure: 'running' })
})
