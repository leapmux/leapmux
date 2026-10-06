import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

deepseekHarnessTest('clears native context and preserves stored Worker rows', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseSessionReset(context)
})
