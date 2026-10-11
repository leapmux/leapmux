import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { reasonixTest } from '../reasonix-fixtures'
import { relatedNativeProof } from './scenarios'

reasonixTest('proves the native swarm-mode limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => relatedNativeProof(native) })
})
