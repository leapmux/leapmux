import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { lettaTest } from '../letta-fixtures'
import { runningChild } from './scenarios'

lettaTest('refuses native child interrupt while the original child task still runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => runningChild(native) })
})
