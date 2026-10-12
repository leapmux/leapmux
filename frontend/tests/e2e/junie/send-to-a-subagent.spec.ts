import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { junieTest } from '../junie-fixtures'
import { runningChild } from './scenarios'

junieTest('refuses native child send while the original child task still runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => runningChild(native) })
})
