import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { nativeContext } from './scenarios'

deepseekHarnessTest('reads and changes actual file bytes through native tools', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseFileToolExecution(context)
})
