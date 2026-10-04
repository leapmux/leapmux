import { commandCodeTest } from '../command-code-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { nativeContext, runningChild } from './scenarios'

commandCodeTest('refuses native child interrupt while the original child task still runs', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  await expectUnsupportedSubagent(context, { operation: 'interrupt', openChild: () => runningChild(context) })
})
