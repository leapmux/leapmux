import { diracTest } from '../dirac-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { nativeContext, runningChild } from './scenarios'

diracTest('refuses native child interrupt while the original child task still runs', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await expectUnsupportedSubagent(context, { operation: 'interrupt', openChild: () => runningChild(context) })
})
