import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext, runningChild } from './scenarios'

qoderTest('refuses native child send while the original child task still runs', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
  await expectUnsupportedSubagent(context, { operation: 'send', openChild: () => runningChild(context) })
})
