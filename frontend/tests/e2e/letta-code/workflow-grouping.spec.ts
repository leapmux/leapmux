import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { lettaTest } from '../letta-fixtures'
import { nativeContext, runningChild } from './scenarios'

lettaTest('keeps two native child work rows without a workflow group', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseUngroupedNativeChildren(context, options => runningChild(context, options))
})
