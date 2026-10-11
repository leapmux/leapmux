import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { opencodeTest } from '../opencode-fixtures'
import { relatedNativeProof } from './scenarios'

opencodeTest('proves the native swarm-mode limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => relatedNativeProof(native) })
})
