import { codebuddyTest } from '../codebuddy-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { runningChild } from './scenarios'

codebuddyTest('refuses native child interrupt while the original child task still runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'interrupt', openChild: () => runningChild(native) })
})
