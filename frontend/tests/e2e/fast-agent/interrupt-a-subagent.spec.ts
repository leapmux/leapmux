import { fastAgentTest } from '../fastagent-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { runningChild } from './scenarios'

fastAgentTest('refuses native child interrupt while the original child task still runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => runningChild(native) })
})
