import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { qoderTest } from '../qoder-fixtures'
import { relatedNativeProof } from './scenarios'

qoderTest('exposes no separate native swarm mode setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => relatedNativeProof(native) })
})
