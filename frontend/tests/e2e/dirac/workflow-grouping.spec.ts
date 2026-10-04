import { diracTest } from '../dirac-fixtures'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { nativeContext, runningChild } from './scenarios'

diracTest('keeps two native child work rows without a workflow group', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await exerciseUngroupedNativeChildren(context, options => runningChild(context, options))
})
