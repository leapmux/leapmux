import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { junieTest } from '../junie-fixtures'
import { runningChild } from './scenarios'

junieTest('refuses native child interrupt while the original child task still runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => runningChild(native) })
})
