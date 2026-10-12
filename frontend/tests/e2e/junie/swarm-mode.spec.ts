import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { junieTest } from '../junie-fixtures'
import { relatedNativeProof } from './scenarios'

junieTest('exposes no separate native swarm mode setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => relatedNativeProof(native) })
})
