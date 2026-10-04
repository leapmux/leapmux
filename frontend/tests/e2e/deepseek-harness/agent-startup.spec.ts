import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseAgentStartup } from '../helpers/nativeLifecycle'
import { nativeContext, nativeLaunch } from './scenarios'

deepseekHarnessTest('delivers queued startup input and keeps it after a native startup failure', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context) })
  await exerciseAgentStartup(context, { launch: nativeLaunch(context), failed: true })
})
