import { openProfiledNativeChild } from '../helpers/runningChildProof'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { reasonixTest } from '../reasonix-fixtures'
import { REASONIX_CHILD } from './childScenario'

reasonixTest('refuses native child send while the actual child task runs', async ({ native }) => {
  await expectUnsupportedSubagent(native, { operation: 'send', openChild: () => openProfiledNativeChild(native, REASONIX_CHILD) })
})
