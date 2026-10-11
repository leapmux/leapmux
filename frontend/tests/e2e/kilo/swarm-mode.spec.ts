import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { kiloTest } from '../kilo-fixtures'
import { relatedNativeProof } from './scenarios'

kiloTest('proves the native swarm-mode limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => relatedNativeProof(native) })
})
