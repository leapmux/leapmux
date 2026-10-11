import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { kiroTest } from '../kiro-fixtures'
import { relatedNativeProof } from './scenarios'

kiroTest('proves the missing swarm-mode setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => relatedNativeProof(native) })
})
