import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { kiloTest } from '../kilo-fixtures'
import { KILO_CHILD } from './childScenario'

kiloTest('refuses native child send while the actual child task runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => openProfiledNativeChild(native, KILO_CHILD) })
})
