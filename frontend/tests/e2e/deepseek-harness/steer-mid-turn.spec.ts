import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'
import { nativeContext } from './scenarios'

deepseekHarnessTest('delivers steering to the current native turn before its real command ends', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseSteerAfterTool(context, { expectDisplayedOutput: false })
})
