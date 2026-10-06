import { diracTest } from '../dirac-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { runningChild } from './scenarios'

diracTest('refuses native child interrupt while the original child task still runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => runningChild(native) })
})
