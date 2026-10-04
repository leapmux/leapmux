import { commandCodeTest } from '../command-code-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { nativeContext, runningChild } from './scenarios'

commandCodeTest('refuses native child send while the original child task still runs', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  await expectUnsupportedSubagent(context, { operation: 'send', openChild: () => runningChild(context) })
})
