import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { qoderTest } from '../qoder-fixtures'
import { runningChild } from './scenarios'

qoderTest('refuses native child interrupt while the original child task still runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => runningChild(native) })
})
