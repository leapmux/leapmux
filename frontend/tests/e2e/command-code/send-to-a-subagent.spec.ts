import { commandCodeTest } from '../command-code-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { runningChild } from './scenarios'

commandCodeTest('refuses native child send while the original child task still runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => runningChild(native) })
})
