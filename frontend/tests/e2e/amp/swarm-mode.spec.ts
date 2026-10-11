import { ampTest } from '../amp-fixtures'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

ampTest('proves the missing swarm-mode setting against the live catalog and a native tool', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => relatedNativeProof(native) })
})
