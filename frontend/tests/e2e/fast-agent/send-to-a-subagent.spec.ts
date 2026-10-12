import { fastAgentTest } from '../fastagent-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { runningChild } from './scenarios'

fastAgentTest('refuses native child send while the original child task still runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => runningChild(native) })
})
