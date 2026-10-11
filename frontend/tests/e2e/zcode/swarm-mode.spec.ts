import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { zcodeTest } from '../zcode-fixtures'
import { relatedNativeProof } from './scenarios'

zcodeTest('proves the native swarm-mode limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'swarm-mode', relatedProof: () => relatedNativeProof(native) })
})
