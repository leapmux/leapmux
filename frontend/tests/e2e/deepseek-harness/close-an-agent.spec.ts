import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

deepseekHarnessTest('stops the native process and its owned command processes', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseCloseAgent(context)
})
