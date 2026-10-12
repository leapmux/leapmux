import { droidTest } from '../droid-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { runningChild } from './scenarios'

droidTest('refuses native child interrupt while the original child task still runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => runningChild(native) })
})
