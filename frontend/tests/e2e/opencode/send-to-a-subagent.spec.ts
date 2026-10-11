import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { opencodeTest } from '../opencode-fixtures'
import { OPENCODE_CHILD } from './childScenario'

opencodeTest('refuses native child send while the actual child task runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => openProfiledNativeChild(native, OPENCODE_CHILD) })
})
