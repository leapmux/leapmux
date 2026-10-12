import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { lettaTest } from '../letta-fixtures'
import { relatedNativeProof } from './scenarios'

lettaTest('exposes no separate native swarm mode setting', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => relatedNativeProof(native) })
})
