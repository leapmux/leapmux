import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { nativeContext, runningChild } from './scenarios'

fastAgentTest('keeps two native child work rows without a workflow group', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await exerciseUngroupedNativeChildren(context, options => runningChild(context, options))
})
